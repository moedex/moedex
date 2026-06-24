package symbol

import "testing"

// A realistic CFML include mixing tag UDFs, a cfscript UDF, page JavaScript (a
// trap the extractor must NOT pick up), and a component. Modeled on the
// hugedomains _inc/*.cfm shape: <cffunction name="..."> tag functions.
var cfSrc = []byte(`<cfcomponent displayname="OrderService" output="false">

<cffunction name="voidTransaction" access="public" output="false" returntype="numeric">
	<cfargument name="_pnRef" default="">
	<cfif not len(arguments._pnRef)>
		<cfreturn 0>
	</cfif>
	<cfreturn 1>
</cffunction>

<cffunction
	access="public"
	name="chargeOrder"
	returntype="boolean">
	<cfscript>
		function normalizeAmount(raw) {
			return val(raw);
		}
	</cfscript>
	<cfreturn true>
</cffunction>

</cfcomponent>

<script type="text/javascript">
	function trackPageView(id) { return id; }
</script>
`)

func TestCFExtractor_TagFunctionsAndComponent(t *testing.T) {
	syms, err := CFExtractor{}.Extract(cfSrc)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	by := symNames(syms)

	for _, n := range []string{"voidTransaction", "chargeOrder"} {
		s, ok := by[n]
		if !ok {
			t.Errorf("missing cffunction %q; got %v", n, names(syms))
			continue
		}
		if s.Kind != Func {
			t.Errorf("%q: kind = %v, want Func", n, s.Kind)
		}
		if got := string(cfSrc[s.NameStart:s.NameEnd]); got != n {
			t.Errorf("%q: content[NameStart:NameEnd] = %q, want %q", n, got, n)
		}
		if s.BodyStart < 0 || s.BodyEnd > len(cfSrc) || s.BodyStart >= s.BodyEnd {
			t.Errorf("%q: invalid body range [%d,%d)", n, s.BodyStart, s.BodyEnd)
		}
		if !(s.BodyStart <= s.NameStart && s.NameEnd <= s.BodyEnd) {
			t.Errorf("%q: name not within body", n)
		}
	}

	// The component name comes from displayname.
	if s, ok := by["OrderService"]; !ok {
		t.Errorf("missing cfcomponent OrderService; got %v", names(syms))
	} else if s.Kind != Type {
		t.Errorf("OrderService: kind = %v, want Type", s.Kind)
	}

	// The cfscript UDF inside <cfscript> IS extracted.
	if _, ok := by["normalizeAmount"]; !ok {
		t.Errorf("missing cfscript UDF normalizeAmount; got %v", names(syms))
	}

	// The page JavaScript function is NOT inside <cfscript> and must be ignored.
	if _, ok := by["trackPageView"]; ok {
		t.Errorf("page JavaScript trackPageView was wrongly extracted as a CF symbol")
	}
}

func TestCFExtractor_BodyEndAtCloseTag(t *testing.T) {
	syms, _ := CFExtractor{}.Extract(cfSrc)
	by := symNames(syms)
	s := by["voidTransaction"]
	// The body must end at this function's </cffunction>, not run into the next
	// function: chargeOrder's name must fall OUTSIDE voidTransaction's range.
	charge := by["chargeOrder"]
	if charge.NameStart >= s.BodyStart && charge.NameStart < s.BodyEnd {
		t.Errorf("voidTransaction body [%d,%d) swallowed chargeOrder at %d (close-tag match failed)",
			s.BodyStart, s.BodyEnd, charge.NameStart)
	}
}

func TestCFExtractor_Enclosing(t *testing.T) {
	syms, _ := CFExtractor{}.Extract(cfSrc)
	ix := NewIndex()
	ix.Set(0, syms)
	// An offset inside voidTransaction's body resolves to it.
	off := indexOf(cfSrc, "arguments._pnRef")
	s, ok := ix.Enclosing(0, off)
	if !ok {
		t.Fatalf("Enclosing found nothing at offset %d", off)
	}
	if s.Name != "voidTransaction" {
		t.Errorf("Enclosing = %q, want voidTransaction", s.Name)
	}
}

func TestCFExtractor_SingleQuotesAndCase(t *testing.T) {
	src := []byte("<CFFUNCTION NAME='getOwner' ACCESS='public'>\n<cfreturn 1>\n</CFFUNCTION>\n")
	syms, _ := CFExtractor{}.Extract(src)
	if _, ok := symNames(syms)["getOwner"]; !ok {
		t.Errorf("uppercase/single-quote <CFFUNCTION NAME='getOwner'> not extracted; got %v", names(syms))
	}
}

func TestCFExtractor_NeverPanicsOnGarbage(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte(""),
		[]byte(`<cffunction name="unterminated">`),
		[]byte(`<cffunction name=>`),
		[]byte(`<cfscript> function ( {{{ "unterminated`),
		[]byte(`<cfcomponent displayname="X">`),
	}
	for i, in := range inputs {
		syms, err := CFExtractor{}.Extract(in)
		if err != nil {
			t.Errorf("input %d: unexpected error %v", i, err)
		}
		for _, s := range syms {
			if s.BodyStart < 0 || s.BodyEnd > len(in) || s.NameStart < 0 || s.NameEnd > len(in) {
				t.Errorf("input %d: out-of-bounds offsets in %+v (len %d)", i, s, len(in))
			}
		}
	}
}
