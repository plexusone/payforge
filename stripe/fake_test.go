package stripe_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/plexusone/payforge/stripe"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// secretKey is an obviously fake key; tests check it never appears in errors.
const secretKey = "sk_test_do_not_leak_this_value" //nolint:gosec // G101: a fake key for a test

// call is one request the fake received.
type call struct {
	Method, Path  string
	Form          map[string]string
	Auth, IdemKey string
}

// fake stands in for the Stripe API.
type fake struct {
	t  *testing.T
	mu sync.Mutex

	calls []call
	// prices maps a lookup key to a price ID; a missing key lists nothing.
	prices map[string]string
	// customers is the result of a customer search.
	customers []string
	// subscriptions is the status of each subscription the customer has.
	subscriptions []string
	// products and priceObjs hold what a sync reads and writes, by product ID
	// and by lookup key. A GET of a price whose key is in priceObjs returns it;
	// otherwise prices holds just an ID.
	products  map[string]map[string]any
	priceObjs map[string]map[string]any
	nextPrice int
	// fail makes the named "METHOD path" answer with a Stripe error.
	fail map[string]int
}

func newFake(t *testing.T) (*fake, *httptest.Server) {
	t.Helper()
	f := &fake{t: t, prices: map[string]string{}, fail: map[string]int{},
		products: map[string]map[string]any{}, priceObjs: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		f.t.Errorf("parse form: %v", err)
	}
	form := map[string]string{}
	for k, v := range r.Form {
		form[k] = v[0]
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, r.URL.Path, form, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if status, ok := f.fail[r.Method+" "+r.URL.Path]; ok {
		code := "parameter_invalid_empty"
		if status == http.StatusNotFound {
			code = "resource_missing"
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"` + code + `","message":"Something is wrong"}}`))
		return
	}
	list := func(url string, data []map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "url": url, "has_more": false, "data": data})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/v1/products/") {
		id := strings.TrimPrefix(r.URL.Path, "/v1/products/")
		prod, ok := f.products[id]
		switch {
		case r.Method == "GET" && !ok:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such product"}}`))
		case r.Method == "GET":
			_ = json.NewEncoder(w).Encode(prod)
		case r.Method == "POST" && ok: // update
			if v, has := form["name"]; has {
				prod["name"] = v
			}
			meta, _ := prod["metadata"].(map[string]any)
			for k, v := range form {
				if strings.HasPrefix(k, "metadata[") {
					meta[strings.TrimSuffix(strings.TrimPrefix(k, "metadata["), "]")] = v
				}
			}
			_ = json.NewEncoder(w).Encode(prod)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /v1/products":
		meta := map[string]any{}
		for k, v := range form {
			if strings.HasPrefix(k, "metadata[") {
				meta[strings.TrimSuffix(strings.TrimPrefix(k, "metadata["), "]")] = v
			}
		}
		f.products[form["id"]] = map[string]any{"id": form["id"], "object": "product", "name": form["name"], "active": true, "metadata": meta}
		_ = json.NewEncoder(w).Encode(f.products[form["id"]])
	case "POST /v1/prices":
		f.nextPrice++
		meta := map[string]any{}
		for k, v := range form {
			if strings.HasPrefix(k, "metadata[") {
				meta[strings.TrimSuffix(strings.TrimPrefix(k, "metadata["), "]")] = v
			}
		}
		amount, _ := strconv.ParseInt(form["unit_amount"], 10, 64)
		obj := map[string]any{"id": fmt.Sprintf("price_new_%d", f.nextPrice), "object": "price", "active": true,
			"unit_amount": amount, "currency": form["currency"], "product": form["product"],
			"lookup_key": form["lookup_key"], "metadata": meta}
		if iv, ok := form["recurring[interval]"]; ok {
			obj["recurring"] = map[string]any{"interval": iv, "interval_count": 1}
		}
		f.priceObjs[form["lookup_key"]] = obj
		_ = json.NewEncoder(w).Encode(obj)
	case "GET /v1/prices":
		var data []map[string]any
		if obj, ok := f.priceObjs[form["lookup_keys[0]"]]; ok {
			data = append(data, obj)
		} else if id, ok := f.prices[form["lookup_keys[0]"]]; ok {
			data = append(data, map[string]any{"id": id, "object": "price", "lookup_key": form["lookup_keys[0]"]})
		}
		list("/v1/prices", data)
	case "GET /v1/customers/search":
		var data []map[string]any
		for _, id := range f.customers {
			data = append(data, map[string]any{"id": id, "object": "customer"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "search_result", "url": "/v1/customers/search", "has_more": false, "data": data})
	case "POST /v1/customers":
		_, _ = w.Write([]byte(`{"id":"cus_new","object":"customer"}`))
	case "GET /v1/subscriptions":
		var data []map[string]any
		for _, s := range f.subscriptions {
			data = append(data, map[string]any{"id": "sub_1", "object": "subscription", "status": s})
		}
		list("/v1/subscriptions", data)
	case "POST /v1/checkout/sessions":
		_, _ = w.Write([]byte(`{"id":"cs_test_1","object":"checkout.session","url":"https://checkout.example/cs_test_1"}`))
	case "POST /v1/billing_portal/sessions":
		_, _ = w.Write([]byte(`{"id":"bps_1","object":"billing_portal.session","url":"https://portal.example/bps_1"}`))
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// last returns the most recent call to a path, or fails the test.
func (f *fake) last(method, path string) call {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method && f.calls[i].Path == path {
			return f.calls[i]
		}
	}
	f.t.Fatalf("no %s %s call; got %+v", method, path, f.calls)
	return call{}
}

func (f *fake) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			n++
		}
	}
	return n
}

func load(t *testing.T, name string) *pricingspec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := pricingspec.Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return s
}

func newClient(t *testing.T, spec *pricingspec.Spec, srv *httptest.Server) *stripe.Client {
	t.Helper()
	c, err := stripe.New(spec, stripe.Config{SecretKey: secretKey, APIURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
