package espa

// This file implements the receiving side of ESPA-X 2.0, the
// XML-over-TCP successor of ESPA 4.4.4 used by nurse-call/DECT/paging
// vendors. Each message is one <ESPA-X version="2.0"> document, commonly
// framed STX ... ETX on the wire (bare documents are tolerated too). We
// play a minimal service-provider role: accept REQ.LOGIN, answer
// REQ.HEARTBEAT keepalives, and turn REQ.P-CALL paging requests into
// events, acknowledged with REP.* state="ok" replies.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// espaxClosing is the root closing tag bare (unframed) documents are
// scanned for.
const espaxClosing = "</ESPA-X>"

// maxPayloadXML caps how much raw request XML is archived in the payload.
const maxPayloadXML = 8 * 1024

// xmlNode is a generic lenient XML tree: vendors disagree on the exact
// tag set inside CP-CALL, so we keep everything and search by name.
type xmlNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	Nodes   []xmlNode  `xml:",any"`
}

// serveESPAX runs one ESPA-X session on conn: read a document, dispatch
// every REQ.* inside it, reply in the same framing, repeat.
func serveESPAX(conn net.Conn, defSev model.Severity, emit emitFunc, log *slog.Logger) error {
	br := bufio.NewReader(deadlineReader{conn})
	for {
		doc, framed, err := readESPAXDoc(br)
		if err != nil {
			return sessionErr(err)
		}
		if err := handleESPAXDoc(conn, doc, framed, defSev, emit, log); err != nil {
			return err
		}
	}
}

// readESPAXDoc scans the stream for the next document: either
// STX <xml> ETX (the common ESPA-X framing) or a bare document starting
// at '<' and ending with the </ESPA-X> closing tag. Anything between
// documents (CR/LF, filler) is skipped.
func readESPAXDoc(br *bufio.Reader) (doc []byte, framed bool, err error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return nil, false, err
		}
		switch b {
		case stx: // framed: collect until ETX
			var buf []byte
			for {
				c, err := br.ReadByte()
				if err != nil {
					return nil, false, err
				}
				if c == etx {
					return buf, true, nil
				}
				buf = append(buf, c)
				if len(buf) > maxFrameBytes {
					return nil, false, errFrameTooLarge
				}
			}
		case '<': // bare: collect until the root closing tag
			buf := []byte{'<'}
			closing := []byte(espaxClosing)
			for !bytes.HasSuffix(buf, closing) {
				c, err := br.ReadByte()
				if err != nil {
					return nil, false, err
				}
				buf = append(buf, c)
				if len(buf) > maxFrameBytes {
					return nil, false, errFrameTooLarge
				}
			}
			return buf, false, nil
		default: // inter-document filler — ignore
		}
	}
}

// handleESPAXDoc parses one document and answers every request element
// under the ESPA-X root. Unparseable documents are logged and skipped
// (framing re-synchronises on the next STX / '<').
func handleESPAXDoc(conn net.Conn, doc []byte, framed bool, defSev model.Severity, emit emitFunc, log *slog.Logger) error {
	var root xmlNode
	if err := xml.Unmarshal(doc, &root); err != nil {
		log.Warn("espa-x: unparseable document ignored", "err", err)
		return nil
	}
	// Normally root is ESPA-X and requests are its children; tolerate a
	// request element as the document root too.
	nodes := root.Nodes
	if strings.HasPrefix(root.XMLName.Local, "REQ.") {
		nodes = []xmlNode{root}
	}
	for i := range nodes {
		req := &nodes[i]
		switch req.XMLName.Local {
		case "REQ.LOGIN":
			// Minimal provider: every login is accepted (we do not
			// implement the user/password handshake of the full spec).
			if err := writeESPAXReply(conn, framed, `<REP.LOGIN state="ok"/>`); err != nil {
				return err
			}
		case "REQ.HEARTBEAT":
			if err := writeESPAXReply(conn, framed, `<REP.HEARTBEAT state="ok"/>`); err != nil {
				return err
			}
		case "REQ.P-CALL":
			norm, callID := espaxCallEvent(req, doc, defSev)
			rep := `<REP.P-CALL state="ok"/>`
			if callID != "" {
				rep = `<REP.P-CALL state="ok"><CALL-ID>` + xmlEscape(callID) + `</CALL-ID></REP.P-CALL>`
			}
			if err := writeESPAXReply(conn, framed, rep); err != nil {
				return err
			}
			emit(norm)
		default:
			// REQ.S-REGISTER, REQ.STATUS, REP.* echoes … — outside the
			// implemented subset; log and stay silent.
			log.Debug("espa-x: unhandled element ignored", "element", req.XMLName.Local)
		}
	}
	return nil
}

// espaxCallEvent extracts the CP-CALL fields of a REQ.P-CALL into a
// NormEvent, tolerating the vendor tag variants (ADDRESS/CALL-ADDRESS,
// DISPLAY-MSG/TEXT-MSG).
func espaxCallEvent(req *xmlNode, doc []byte, defSev model.Severity) (*model.NormEvent, string) {
	callID := findText(req, "CALL-ID")
	address := findText(req, "ADDRESS", "CALL-ADDRESS")
	message := findText(req, "DISPLAY-MSG", "TEXT-MSG")
	prio := findText(req, "CALL-PRIO")
	signal := findText(req, "SIGNAL-TYP")

	summary := truncate(message, maxSummaryChars)
	if summary == "" {
		summary = "ESPA-X call"
		if address != "" {
			summary += " " + address
		}
	}

	labels := model.Labels{}
	setLabel(labels, "espa.callId", callID)
	setLabel(labels, "espa.address", address)
	setLabel(labels, "espa.prio", prio)
	setLabel(labels, "espa.signal", signal)

	dedup := ""
	if callID != "" {
		// Retransmissions of the same call (client retries a lost REP)
		// carry the same CALL-ID and dedup into one alert.
		dedup = "espa-x/" + callID
	}

	payload, _ := json.Marshal(struct {
		CallID  string `json:"callId,omitempty"`
		Address string `json:"address,omitempty"`
		Message string `json:"message,omitempty"`
		Prio    string `json:"prio,omitempty"`
		Signal  string `json:"signal,omitempty"`
		XML     string `json:"xml,omitempty"`
	}{callID, address, message, prio, signal, truncate(string(doc), maxPayloadXML)})

	return &model.NormEvent{
		DedupKey: dedup,
		Severity: espaxSeverity(prio, defSev),
		Summary:  summary,
		Labels:   labels,
		Payload:  payload,
	}, callID
}

// espaxSeverity maps CALL-PRIO strings ("prio-1", "alarm", "high", "2",
// …) to a severity: alarm-ish → critical, high-ish → warning, everything
// else → the source-configured default.
func espaxSeverity(prio string, defSev model.Severity) model.Severity {
	p := strings.ToLower(strings.TrimSpace(prio))
	switch {
	case p == "":
		return defSev
	case strings.Contains(p, "alarm"), strings.Contains(p, "1"):
		return model.SevCritical
	case strings.Contains(p, "high"), strings.Contains(p, "2"):
		return model.SevWarning
	default:
		return defSev
	}
}

// findText depth-first searches node for the first element named one of
// names and returns its trimmed text, falling back to a V="…" value
// attribute (some vendor dialects carry values in attributes).
func findText(node *xmlNode, names ...string) string {
	for i := range node.Nodes {
		child := &node.Nodes[i]
		for _, name := range names {
			if child.XMLName.Local != name {
				continue
			}
			if text := strings.TrimSpace(child.Text); text != "" {
				return text
			}
			for _, attr := range child.Attrs {
				if attr.Name.Local == "V" && strings.TrimSpace(attr.Value) != "" {
					return strings.TrimSpace(attr.Value)
				}
			}
		}
		if text := findText(child, names...); text != "" {
			return text
		}
	}
	return ""
}

// writeESPAXReply wraps inner in the ESPA-X root document and sends it,
// framed with STX/ETX when the request was framed, bare otherwise.
func writeESPAXReply(conn net.Conn, framed bool, inner string) error {
	doc := `<ESPA-X version="2.0" timestamp="` +
		time.Now().UTC().Format(time.RFC3339) + `">` + inner + `</ESPA-X>`
	if framed {
		return write(conn, append(append([]byte{stx}, doc...), etx))
	}
	return write(conn, []byte(doc))
}

// xmlEscape escapes text for embedding in a reply document.
func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
