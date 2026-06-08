package main

import (
	"fmt"
	"strings"
)

// checklist renders the manual follow-up steps. The generator stamps the
// stubs that are pure additions (new files compile on their own); the
// remaining wiring means editing hand-maintained registries (api.go's
// registerAll(), bundle.go's KindOrder, api.ts's invalidations map, the
// frontend router). Those are small, fiddly, and a blind rewrite is more
// likely to break the build than help — so np-gen prints the exact
// snippet and location for a human or agent to paste, per house policy.
func checklist(n Names, opt options) string {
	var b strings.Builder
	b.WriteString("Manual follow-up (paste these — np-gen does not edit hand-maintained registries):\n\n")

	step := 0
	next := func(title string) {
		step++
		fmt.Fprintf(&b, "  %d. %s\n", step, title)
	}

	next("Register the REST routes — internal/api/api.go, in registerAll():")
	fmt.Fprintf(&b, "       a.register%s()\n\n", n.PascalPlural)

	next("Add the bundle kind to the apply-order vocabulary — internal/bundle/bundle.go, KindOrder:")
	fmt.Fprintf(&b, "       // append (or slot by dependency order) the PascalCase kind:\n")
	fmt.Fprintf(&b, "       %q,\n\n", n.Pascal)

	next("Add the SSE invalidation key so live updates refresh the list — web/src/api.ts, invalidations map:")
	fmt.Fprintf(&b, "       // the config event already fans out; add the query key to its list:\n")
	fmt.Fprintf(&b, "       config: [['objects'], ['rules'], ['resources'], ['%s']],\n\n", n.KebabPlural)

	next("Wire the page into the router/nav and the i18n labels — wherever pages register")
	fmt.Fprintf(&b, "       (e.g. web/src/main.tsx routes): import { %sPage } from './pages/%s'\n\n",
		n.PascalPlural, n.PascalPlural)

	next("Fold the generated type into the canonical barrel — move the interface from")
	if opt.out != "" {
		fmt.Fprintf(&b, "       gen_%s.ts into web/src/types.ts, then delete the generated stub.\n\n", n.Kebab)
	} else {
		fmt.Fprintf(&b, "       web/src/types/gen_%s.ts into web/src/types.ts, then delete the stub.\n\n", n.Kebab)
	}

	next("Edit the stubs to add your real fields:")
	fmt.Fprintf(&b, "       - internal/model/gen_%s.go         (domain fields on %s)\n", n.Snake, n.Pascal)
	fmt.Fprintf(&b, "       - web/src/pages/%s.tsx%s(form fields)\n",
		n.PascalPlural, strings.Repeat(" ", max(1, 18-len(n.PascalPlural))))
	b.WriteString("\n")

	b.WriteString("Optional: add validateResourceDoc() case in internal/api/objects.go for\n")
	fmt.Fprintf(&b, "kind-specific server-side validation (storage.%s), and a Go test under\n", n.ConstName)
	b.WriteString("internal/api/ mirroring resources_test.go.\n")

	b.WriteString("\nAfter wiring: `go build ./...` && `cd web && npm run build` should stay green.\n")
	return b.String()
}
