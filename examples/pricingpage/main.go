// Command pricingpage serves a pricing page drawn from a pricing spec, so the
// page and the billing cannot disagree about what a plan costs or includes.
//
//	go run ./examples/pricingpage -spec examples/catalog/pricing.json
package main

import (
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

var page = template.Must(template.New("page").Funcs(template.FuncMap{
	"money": func(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) },
}).Parse(`<!doctype html>
<meta charset="utf-8">
<title>{{.App}} pricing</title>
<h1>Pricing</h1>
<table>
<tr><th></th>{{range .Plans}}<th>{{.Name}}</th>{{end}}</tr>
<tr><td>Price</td>{{range .Plans}}<td>
{{- if .Free}}Free{{else if not .SelfServe}}Contact us{{else}}{{range .Prices}}{{money .AmountCents}}/{{.Interval}} {{end}}{{end -}}
{{if .TrialDays}} ({{.TrialDays}}-day trial){{end}}</td>{{end}}</tr>
{{range .Features}}<tr><td>{{.Name}}</td>{{range .Cells}}<td>{{if .Included}}{{if .Level}}{{.Level}}{{else}}yes{{end}}{{else}}-{{end}}</td>{{end}}</tr>
{{end}}
{{range .Limits}}<tr><td>{{.Name}}</td>{{range .Cells}}<td>{{if .Unlimited}}unlimited{{else}}{{.Max}}{{end}}</td>{{end}}</tr>
{{end}}
</table>
`))

// Render writes the pricing page for a spec.
func Render(w io.Writer, spec *pricingspec.Spec) error {
	return page.Execute(w, spec.PricingView())
}

func main() {
	path := flag.String("spec", "pricing.json", "the pricing spec")
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	flag.Parse()
	f, err := os.Open(*path) //nolint:gosec // G304: the path is the command's argument
	if err != nil {
		log.Fatal(err)
	}
	spec, err := pricingspec.Load(f)
	_ = f.Close() // read-only file; the spec is already loaded
	if err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := Render(w, spec); err != nil {
			log.Printf("render: %v", err)
		}
	})
	srv := &http.Server{Addr: *addr, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
