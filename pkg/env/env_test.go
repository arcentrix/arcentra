package env

import (
	"reflect"
	"testing"
	"time"
)

func TestGetEnvInt(t *testing.T) {
	t.Setenv("ARCENTRA_TEST_INT", "42")
	if got := GetEnvInt("ARCENTRA_TEST_INT", 7); got != 42 {
		t.Fatalf("GetEnvInt valid value = %d, want 42", got)
	}

	t.Setenv("ARCENTRA_TEST_INT", "not-int")
	if got := GetEnvInt("ARCENTRA_TEST_INT", 7); got != 7 {
		t.Fatalf("GetEnvInt invalid value = %d, want 7", got)
	}

	t.Setenv("ARCENTRA_TEST_INT", "")
	if got := GetEnvInt("ARCENTRA_TEST_INT", 7); got != 7 {
		t.Fatalf("GetEnvInt empty value = %d, want 7", got)
	}
}

func TestGetEnvBool(t *testing.T) {
	t.Setenv("ARCENTRA_TEST_BOOL", "true")
	if got := GetEnvBool("ARCENTRA_TEST_BOOL", false); got != true {
		t.Fatalf("GetEnvBool true = %v, want true", got)
	}

	t.Setenv("ARCENTRA_TEST_BOOL", "FALSE")
	if got := GetEnvBool("ARCENTRA_TEST_BOOL", true); got != false {
		t.Fatalf("GetEnvBool false = %v, want false", got)
	}

	t.Setenv("ARCENTRA_TEST_BOOL", "not-bool")
	if got := GetEnvBool("ARCENTRA_TEST_BOOL", true); got != true {
		t.Fatalf("GetEnvBool invalid = %v, want true", got)
	}
}

func TestGetEnvFloat64(t *testing.T) {
	t.Setenv("ARCENTRA_TEST_FLOAT", "3.14")
	if got := GetEnvFloat64("ARCENTRA_TEST_FLOAT", 1.0); got != 3.14 {
		t.Fatalf("GetEnvFloat64 valid = %v, want 3.14", got)
	}

	t.Setenv("ARCENTRA_TEST_FLOAT", "not-float")
	if got := GetEnvFloat64("ARCENTRA_TEST_FLOAT", 1.0); got != 1.0 {
		t.Fatalf("GetEnvFloat64 invalid = %v, want 1.0", got)
	}
}

func TestGetEnvDuration(t *testing.T) {
	t.Setenv("ARCENTRA_TEST_DURATION", "1h2m3s")
	if got := GetEnvDuration("ARCENTRA_TEST_DURATION", 5*time.Second); got != time.Hour+2*time.Minute+3*time.Second {
		t.Fatalf("GetEnvDuration valid = %v, want %v", got, time.Hour+2*time.Minute+3*time.Second)
	}

	t.Setenv("ARCENTRA_TEST_DURATION", "not-duration")
	if got := GetEnvDuration("ARCENTRA_TEST_DURATION", 5*time.Second); got != 5*time.Second {
		t.Fatalf("GetEnvDuration invalid = %v, want %v", got, 5*time.Second)
	}
}

func TestGetEnvString(t *testing.T) {
	t.Setenv("ARCENTRA_TEST_STRING", "hello")
	if got := GetEnvString("ARCENTRA_TEST_STRING", "default"); got != "hello" {
		t.Fatalf("GetEnvString valid = %q, want %q", got, "hello")
	}

	t.Setenv("ARCENTRA_TEST_STRING", "")
	if got := GetEnvString("ARCENTRA_TEST_STRING", "default"); got != "default" {
		t.Fatalf("GetEnvString empty = %q, want %q", got, "default")
	}
}

func TestGetEnvStringSlice(t *testing.T) {
	t.Setenv("ARCENTRA_TEST_STRING_SLICE", "a,b,c")
	want := []string{"a", "b", "c"}
	if got := GetEnvStringSlice("ARCENTRA_TEST_STRING_SLICE", []string{"x"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetEnvStringSlice valid = %v, want %v", got, want)
	}

	t.Setenv("ARCENTRA_TEST_STRING_SLICE", "")
	def := []string{"x"}
	if got := GetEnvStringSlice("ARCENTRA_TEST_STRING_SLICE", def); !reflect.DeepEqual(got, def) {
		t.Fatalf("GetEnvStringSlice empty = %v, want %v", got, def)
	}
}

func TestGetEnvStringMap(t *testing.T) {
	t.Setenv("ARCENTRA_TEST_STRING_MAP", "a=1,b=2,invalid")
	want := map[string]string{"a": "1", "b": "2"}
	if got := GetEnvStringMap("ARCENTRA_TEST_STRING_MAP", map[string]string{"x": "y"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetEnvStringMap valid = %v, want %v", got, want)
	}

	t.Setenv("ARCENTRA_TEST_STRING_MAP", "")
	def := map[string]string{"x": "y"}
	if got := GetEnvStringMap("ARCENTRA_TEST_STRING_MAP", def); !reflect.DeepEqual(got, def) {
		t.Fatalf("GetEnvStringMap empty = %v, want %v", got, def)
	}
}

func TestGetEnvStringMapSlice(t *testing.T) {
	t.Setenv("ARCENTRA_TEST_STRING_MAP_SLICE", "a=1,b=2,invalid")
	want := map[string][]string{"a": {"1"}, "b": {"2"}}
	if got := GetEnvStringMapSlice("ARCENTRA_TEST_STRING_MAP_SLICE", map[string][]string{"x": {"y"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetEnvStringMapSlice valid = %v, want %v", got, want)
	}

	t.Setenv("ARCENTRA_TEST_STRING_MAP_SLICE", "")
	def := map[string][]string{"x": {"y"}}
	if got := GetEnvStringMapSlice("ARCENTRA_TEST_STRING_MAP_SLICE", def); !reflect.DeepEqual(got, def) {
		t.Fatalf("GetEnvStringMapSlice empty = %v, want %v", got, def)
	}
}
