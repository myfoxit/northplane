package espa

// This file implements ESPA 4.4.4 — the 1984 ESPA "Serial Data
// Interface" paging protocol (SOH/STX/ETX framed records over an async
// serial line), here received over a raw TCP socket the way
// serial-device servers (Moxa & friends) bridge it. We act as the called
// party (slave): answer polls with ACK, verify the BCC of every data
// block, ACK/NAK it, and turn "call to pager" blocks into events.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/northplane/northplane/internal/model"
)

// ESPA 4.4.4 control characters (ISO 1745 basic mode).
const (
	soh byte = 0x01 // start of heading (frame start)
	stx byte = 0x02 // start of text
	etx byte = 0x03 // end of text
	eot byte = 0x04 // end of transmission (transaction reset)
	enq byte = 0x05 // enquiry (poll/selection)
	ack byte = 0x06 // acknowledge
	nak byte = 0x15 // negative acknowledge
	rs  byte = 0x1E // record separator between data records
	us  byte = 0x1F // unit separator between record id and value
)

// ESPA 4.4.4 record identifiers inside a "call to pager" data block.
const (
	recCallAddress = "1" // pager number
	recDisplayMsg  = "2" // display message
	recBeepCoding  = "3" // beep coding 1..8
	recCallType    = "4" // 1=reset, 2=speech, 3=standard
	recTransmits   = "5" // number of transmissions
	recPriority    = "6" // 1=alarm, 2=high, 3=normal
)

// fnCallToPager is the header function code of a paging call. Other
// function codes (status information/request) are ACKed but not turned
// into events.
const fnCallToPager = byte('1')

var errFrameTooLarge = errors.New("espa: frame exceeds size cap")

// serveESPA runs one ESPA 4.4.4 session on conn until the peer closes,
// the rolling read deadline fires, or a frame breaks the size cap.
//
// The state machine is deliberately liberal (real-world bridges differ):
//   - ENQ answers ACK, whether it arrives bare or after selection
//     address characters ('1' ENQ etc.) — we do not implement full
//     multi-drop addressing, we simply signal ready.
//   - EOT resets the transaction; senders that skip polling and open
//     with SOH directly are handled the same way.
//   - Any other byte outside a frame is ignored (addressing chars,
//     line noise, CR/LF from chatty bridges).
func serveESPA(conn net.Conn, defSev model.Severity, emit emitFunc, log *slog.Logger) error {
	br := bufio.NewReader(deadlineReader{conn})
	for {
		b, err := br.ReadByte()
		if err != nil {
			return sessionErr(err)
		}
		switch b {
		case enq: // poll or selection: signal ready
			if err := write(conn, []byte{ack}); err != nil {
				return err
			}
		case eot: // transaction over; nothing carried across frames
		case soh:
			body, bcc, err := readESPABlock(br)
			if err != nil {
				return sessionErr(err)
			}
			if xorBCC(body) != bcc {
				log.Debug("espa: BCC mismatch, NAK", "want", xorBCC(body), "got", bcc)
				if err := write(conn, []byte{nak}); err != nil {
					return err
				}
				continue
			}
			// Valid block: ACK first (the sender's retry timer is short),
			// then normalise. Publishing happens on the manager's context.
			if err := write(conn, []byte{ack}); err != nil {
				return err
			}
			if norm, ok := parseESPABlock(body, defSev, log); ok {
				emit(norm)
			}
		default: // addressing char / filler outside a frame — ignore
		}
	}
}

// readESPABlock consumes a data block after its SOH: every byte through
// ETX inclusive (the BCC coverage per ESPA 4.4.4 §"block check"), then
// the BCC byte itself.
func readESPABlock(br *bufio.Reader) (body []byte, bcc byte, err error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		body = append(body, b)
		if b == etx {
			break
		}
		if len(body) > maxFrameBytes {
			return nil, 0, errFrameTooLarge
		}
	}
	bcc, err = br.ReadByte()
	if err != nil {
		return nil, 0, err
	}
	return body, bcc, nil
}

// xorBCC computes the ESPA 4.4.4 block check character: the XOR of every
// byte following SOH up to and including ETX.
func xorBCC(body []byte) byte {
	var bcc byte
	for _, b := range body {
		bcc ^= b
	}
	return bcc
}

// parseESPABlock turns a checksum-valid block (header + STX + records +
// ETX) into a NormEvent. Non-"call to pager" function codes and
// structurally broken blocks return ok=false: they were ACKed (the
// checksum held) but produce no event.
func parseESPABlock(body []byte, defSev model.Severity, log *slog.Logger) (*model.NormEvent, bool) {
	i := bytes.IndexByte(body, stx)
	if i < 0 || body[len(body)-1] != etx {
		log.Debug("espa: block without STX/ETX structure ignored")
		return nil, false
	}
	header := body[:i]
	data := body[i+1 : len(body)-1]

	// The header is the function code; in addressed (multi-drop) mode the
	// selection address precedes it, so the last header byte decides. An
	// empty header is treated as a call — be liberal in what we accept.
	if fn := headerFunction(header); fn != 0 && fn != fnCallToPager {
		log.Debug("espa: non-call function code ignored", "function", string(fn))
		return nil, false
	}

	records := parseESPARecords(data)
	address := records[recCallAddress]
	summary := truncate(records[recDisplayMsg], maxSummaryChars)
	if summary == "" {
		summary = "ESPA call"
		if address != "" {
			summary += " " + address
		}
	}

	labels := model.Labels{}
	setLabel(labels, "espa.address", address)
	setLabel(labels, "espa.beep", records[recBeepCoding])
	setLabel(labels, "espa.callType", records[recCallType])
	setLabel(labels, "espa.priority", records[recPriority])

	payload, _ := json.Marshal(struct {
		Function string            `json:"function"`
		Records  map[string]string `json:"records"`
	}{Function: "1", Records: records})

	return &model.NormEvent{
		// DedupKey stays empty on purpose: every paging call is a fresh
		// event (the protocol has no call identity to dedup on).
		Severity: espaSeverity(records[recPriority], defSev),
		Summary:  summary,
		Labels:   labels,
		Payload:  payload,
	}, true
}

// headerFunction extracts the function code: the last non-control byte
// of the header, 0 when the header is empty/all-control.
func headerFunction(header []byte) byte {
	for i := len(header) - 1; i >= 0; i-- {
		if header[i] > 0x20 {
			return header[i]
		}
	}
	return 0
}

// parseESPARecords splits the data section into its RS-separated records
// of the form <record-id> US <value>, decoding values from Latin-1.
func parseESPARecords(data []byte) map[string]string {
	records := map[string]string{}
	for _, rec := range bytes.Split(data, []byte{rs}) {
		if len(rec) == 0 {
			continue
		}
		id, value, found := bytes.Cut(rec, []byte{us})
		if !found {
			continue // record without unit separator: no way to type it
		}
		key := strings.TrimSpace(latin1String(id))
		if key == "" {
			continue
		}
		records[key] = latin1String(value)
	}
	return records
}

// espaSeverity maps the ESPA 4.4.4 priority record to a severity:
// 1 (alarm) → critical, 2 (high) → warning, 3 (normal) and anything
// else → the source-configured default.
func espaSeverity(priority string, defSev model.Severity) model.Severity {
	switch strings.TrimSpace(priority) {
	case "1":
		return model.SevCritical
	case "2":
		return model.SevWarning
	default:
		return defSev
	}
}

// latin1String decodes ISO 8859-1 bytes to UTF-8: every byte 0x80–0xFF
// maps to the same-numbered rune (ESPA 4.4.4 predates Unicode; Latin-1
// is what nurse-call gear sends for umlauts).
func latin1String(b []byte) string {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

// setLabel assigns a truncated label value, skipping empties.
func setLabel(labels model.Labels, key, value string) {
	if value != "" {
		labels[key] = truncate(value, maxLabelValue)
	}
}
