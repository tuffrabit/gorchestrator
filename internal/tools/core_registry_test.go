package tools

import (
	"reflect"
	"testing"
)

func toolNames[T interface{ Name() string }](tools []T) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

var coreBaseNames = []string{"read_file", "list_directory", "grep_search", "write_output", "write_file", "update_file"}

func TestCoreRegistryFullList(t *testing.T) {
	bt := &BoundTools{RootPath: "/tmp", Allowlist: []string{"."}, OutputPath: "out.md"}
	tools, err := NewCoreRegistry(bt)
	if err != nil {
		t.Fatal(err)
	}
	got := toolNames(tools)
	if !reflect.DeepEqual(got, coreBaseNames) {
		t.Fatalf("core registry names = %v, want %v", got, coreBaseNames)
	}
}

func TestCoreRegistryRunTestConditional(t *testing.T) {
	// No test config: run_test absent.
	bt := &BoundTools{RootPath: "/tmp", Allowlist: []string{"."}, OutputPath: "out.md"}
	tools, err := NewCoreRegistry(bt)
	if err != nil {
		t.Fatal(err)
	}
	if hasName(toolNames(tools), "run_test") {
		t.Fatalf("run_test present without Test config")
	}

	// Test config with empty command: run_test absent.
	bt.Test = &TestConfig{Command: "   ", Image: "img"}
	tools, err = NewCoreRegistry(bt)
	if err != nil {
		t.Fatal(err)
	}
	if hasName(toolNames(tools), "run_test") {
		t.Fatalf("run_test present with empty Test.Command")
	}

	// Test config with command: run_test present as the last tool.
	bt.Test = &TestConfig{Command: "make test", Image: "img"}
	tools, err = NewCoreRegistry(bt)
	if err != nil {
		t.Fatal(err)
	}
	got := toolNames(tools)
	if !reflect.DeepEqual(got, append(append([]string{}, coreBaseNames...), "run_test")) {
		t.Fatalf("core registry names with test = %v, want %v plus run_test", got, coreBaseNames)
	}
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestFilterByNamesSubset(t *testing.T) {
	bt := &BoundTools{RootPath: "/tmp", Allowlist: []string{"."}, OutputPath: "out.md"}
	core, err := NewCoreRegistry(bt)
	if err != nil {
		t.Fatal(err)
	}

	// Order-independent subset.
	got := toolNames(FilterByNames(core, []string{"update_file", "grep_search"}))
	if !reflect.DeepEqual(got, []string{"grep_search", "update_file"}) {
		t.Fatalf("FilterByNames subset = %v", got)
	}

	// Unknown names are dropped.
	got = toolNames(FilterByNames(core, []string{"nope", "read_file"}))
	if !reflect.DeepEqual(got, []string{"read_file"}) {
		t.Fatalf("FilterByNames unknown-name drop = %v", got)
	}

	// Empty allow returns everything.
	if toolNames(FilterByNames(core, nil)) == nil || len(FilterByNames(core, nil)) != len(core) {
		t.Fatalf("FilterByNames(empty) should return all %d tools", len(core))
	}
}
