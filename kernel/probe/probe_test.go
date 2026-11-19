package probe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const junit = `<?xml version="1.0" encoding="utf-8"?><testsuites><testsuite name="pytest" errors="1" failures="1" skipped="1" tests="5">
<testcase classname="tests.test_app" name="test_ok" time="0.01"/>
<testcase classname="tests.test_app" name="test_ok2" time="0.01"/>
<testcase classname="tests.test_app" name="test_bad" time="0.01"><failure message="assert 1 == 2">trace</failure></testcase>
<testcase classname="tests.test_policy" name="test_err" time="0.01"><error message="fixture missing">trace</error></testcase>
<testcase classname="tests.test_policy" name="test_skip" time="0"><skipped message="later"/></testcase>
</testsuite></testsuites>`

const coverage = `{"meta":{"version":"7.6"},"files":{
 "app.py":{"summary":{"covered_lines":80,"num_statements":100,"num_branches":20,"covered_branches":10},"missing_lines":[5,6]},
 "policy.py":{"summary":{"covered_lines":10,"num_statements":10,"num_branches":4,"covered_branches":4},"missing_lines":[]}},
 "totals":{"covered_lines":90,"num_statements":110,"num_branches":24,"covered_branches":14}}`

func TestParseReports(t *testing.T) {
	r := &Result{}
	if err := parseJUnit([]byte(junit), r); err != nil {
		t.Fatal(err)
	}
	if r.Tests != 5 || r.Passed != 2 || r.Failed != 1 || r.Errors != 1 || r.Skipped != 1 || len(r.Failures) != 2 {
		t.Fatalf("%+v", r)
	}
	if r.Failures[0].Test != "tests.test_app::test_bad" || r.Failures[0].Message != "assert 1 == 2" {
		t.Fatalf("%+v", r.Failures)
	}
	if err := parseCoverage([]byte(coverage), r); err != nil {
		t.Fatal(err)
	}
	if r.Lines.Percent != 81.82 || r.Branches.Percent != 58.33 || len(r.Files) != 2 || r.Files[0].Path != "app.py" || r.Files[0].Lines.Percent != 80 {
		t.Fatalf("%+v %+v %+v", r.Lines, r.Branches, r.Files)
	}
}

func TestImageTagFollowsRequirements(t *testing.T) {
	d := t.TempDir()
	_ = os.WriteFile(filepath.Join(d, "requirements.txt"), []byte("flask\n"), 0o644)
	a, reqs, _ := ImageTag(Pytest(), d)
	if len(reqs) != 1 {
		t.Fatalf("reqs %v", reqs)
	}
	_ = os.WriteFile(filepath.Join(d, "requirements.txt"), []byte("flask\nrequests\n"), 0o644)
	b, _, _ := ImageTag(Pytest(), d)
	if a == b {
		t.Fatal("tag did not change with the requirements")
	}
}

// TestLivePytest builds a probe image and runs pytest on a small project.
// It needs Podman and network for the build; opt in with ATRIUMCTL_LIVE=1.
func TestLivePytest(t *testing.T) {
	if os.Getenv("ATRIUMCTL_LIVE") == "" {
		t.Skip("set ATRIUMCTL_LIVE=1 to build a probe image and run pytest")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("no podman")
	}
	repo := t.TempDir()
	files := map[string]string{
		"requirements-dev.txt": "pytest\n",
		"calc.py":              "def add(a, b):\n    return a + b\n\ndef sign(x):\n    if x < 0:\n        return -1\n    return 1\n",
		"tests/__init__.py":    "",
		"tests/test_calc.py":   "import calc\n\ndef test_add():\n    assert calc.add(1, 2) == 3\n\ndef test_bad():\n    assert calc.add(1, 1) == 3\n",
	}
	for p, c := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo, p)), 0o755)
		_ = os.WriteFile(filepath.Join(repo, p), []byte(c), 0o444)
	}
	r := &Runner{}
	img, err := r.EnsureImage(context.Background(), Pytest(), repo, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background(), "baseline", Pytest(), img, repo, t.TempDir())
	if res.Error != "" || res.Tests != 2 || res.Passed != 1 || res.Failed != 1 || res.ExitCode != 1 {
		t.Fatalf("%+v", res)
	}
	if len(res.Files) != 1 || res.Files[0].Path != "calc.py" || res.Branches.Total != 2 {
		t.Fatalf("coverage: %+v %+v", res.Files, res.Branches)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "calc.py")); len(b) == 0 {
		t.Fatal("repository changed")
	}
	t.Logf("%d tests, lines %.1f%%, branches %.1f%%, %d ms", res.Tests, res.Lines.Percent, res.Branches.Percent, res.DurationMS)
}
