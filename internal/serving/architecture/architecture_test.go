package architecture_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const modulePath = "github.com/frozenf1sh/fishmesh"

type dependencyRule struct {
	packagePath string
	allowed     map[string]struct{}
}

func TestAtomicDomainDependencies(t *testing.T) {
	rules := []dependencyRule{
		newRule("internal/serving/admission"),
		newRule("internal/serving/backend"),
		newRule("internal/serving/tokenization"),
		newRule("internal/serving/kvcache", "internal/serving/backend"),
		newRule("internal/serving/circuit", "internal/serving/backend"),
		newRule("internal/serving/routing", "internal/serving/backend", "internal/serving/observation"),
		newRule("internal/serving/llmd", "internal/serving/backend", "internal/serving/observation", "internal/serving/routing"),
		newRule("internal/serving/identity", "internal/platform/kubernetes", "internal/serving/backend"),
		newRule("internal/serving/observation", "internal/serving/backend", "internal/serving/identity"),
		newRule("internal/serving/transport", "internal/serving/backend"),
		newRule("internal/serving/discovery", "internal/platform/kubernetes", "internal/serving/backend"),
		newRule("internal/serving/requestpath", "internal/serving/backend", "internal/serving/circuit", "internal/serving/discovery", "internal/serving/kvcache", "internal/serving/observation", "internal/serving/prediction", "internal/serving/routing", "internal/serving/tokenization"),
		newRule("internal/serving/gateway", "internal/serving/admission", "internal/serving/backend", "internal/serving/discovery", "internal/serving/observation", "internal/serving/requestpath", "internal/serving/routing", "internal/serving/transport"),
		newRule("internal/serving/config", "internal/serving/admission", "internal/serving/backend", "internal/serving/circuit", "internal/serving/discovery", "internal/serving/gateway", "internal/serving/identity", "internal/serving/kvcache", "internal/serving/observation", "internal/serving/prediction", "internal/serving/requestpath", "internal/serving/routing", "internal/serving/tokenization", "internal/serving/transport"),
	}

	root := repositoryRoot(t)
	for _, rule := range rules {
		rule := rule
		t.Run(filepath.Base(rule.packagePath), func(t *testing.T) {
			imports := packageImports(t, root, modulePath+"/"+rule.packagePath)
			for _, imported := range imports {
				if !strings.HasPrefix(imported, modulePath+"/internal/") {
					continue
				}
				if _, ok := rule.allowed[imported]; !ok {
					t.Errorf("%s must not import %s", rule.packagePath, strings.TrimPrefix(imported, modulePath+"/"))
				}
			}
		})
	}
}

func newRule(packagePath string, allowed ...string) dependencyRule {
	rule := dependencyRule{packagePath: packagePath, allowed: make(map[string]struct{}, len(allowed))}
	for _, dependency := range allowed {
		rule.allowed[modulePath+"/"+dependency] = struct{}{}
	}
	return rule
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "../../.."))
}

func packageImports(t *testing.T, root, packagePath string) []string {
	t.Helper()
	command := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, packagePath)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list imports for %s: %v", packagePath, err)
	}
	return strings.Fields(string(output))
}
