package gh

import "testing"

func TestNormalizeHunkLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		filename string
		want     string
	}{
		{
			name:     "yaml strips leading whitespace and list marker",
			line:     "-      - uses: actions/checkout@v6",
			filename: ".github/workflows/ci.yml",
			want:     "-uses: actions/checkout@v6",
		},
		{
			name:     "yaml without list marker collapses to same as with marker",
			line:     "-        uses: actions/checkout@v6",
			filename: ".github/workflows/ci.yml",
			want:     "-uses: actions/checkout@v6",
		},
		{
			name:     "yaml addition strips list marker",
			line:     "+      - uses: actions/checkout@v7",
			filename: "deploy.yaml",
			want:     "+uses: actions/checkout@v7",
		},
		{
			name:     "non-yaml keeps list marker, only strips whitespace",
			line:     "-      - uses: actions/checkout@v6",
			filename: "README.md",
			want:     "-- uses: actions/checkout@v6",
		},
		{
			name:     "yaml only strips a single list marker",
			line:     "-   - - nested",
			filename: "config.yml",
			want:     "-- nested",
		},
		{
			name:     "yaml line without marker just strips whitespace",
			line:     "+    name: build",
			filename: "ci.yaml",
			want:     "+name: build",
		},
		{
			name:     "yaml strips tab-separated list marker",
			line:     "-\t-\tuses: actions/checkout@v6",
			filename: "ci.yml",
			want:     "-uses: actions/checkout@v6",
		},
		{
			name:     "uppercase yaml extension is treated as yaml",
			line:     "-      - uses: actions/checkout@v6",
			filename: "CI.YML",
			want:     "-uses: actions/checkout@v6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeHunkLine(tt.line, tt.filename); got != tt.want {
				t.Fatalf("normalizeHunkLine(%q, %q) = %q, want %q", tt.line, tt.filename, got, tt.want)
			}
		})
	}
}

func TestRemoveDependabotTrailingCommand(t *testing.T) {
	input := `Some description of the PR.

More details here.

Dependabot commands and options


You can trigger Dependabot actions by commenting on this PR:
- ` + "`@dependabot rebase`" + ` will rebase this PR
- ` + "`@dependabot recreate`" + ` will recreate this PR, overwriting any edits that have been made to it
- ` + "`@dependabot merge`" + ` will merge this PR after your CI passes on it
- ` + "`@dependabot squash and merge`" + ` will squash and merge this PR after your CI passes on it
- ` + "`@dependabot cancel merge`" + ` will cancel a previously requested merge and block automerging
- ` + "`@dependabot reopen`" + ` will reopen this PR if it is closed
- ` + "`@dependabot close`" + ` will close this PR and stop Dependabot recreating it. You can achieve the same result by closing it manually
- ` + "`@dependabot show  ignore conditions`" + ` will show all of the ignore conditions of the specified dependency
- ` + "`@dependabot ignore  major version`" + ` will close this group update PR and stop Dependabot creating any more for the specific dependency's major version (unless you unignore this specific dependency's major version or upgrade to it yourself)
- ` + "`@dependabot ignore  minor version`" + ` will close this group update PR and stop Dependabot creating any more for the specific dependency's minor version (unless you unignore this specific dependency's minor version or upgrade to it yourself)
- ` + "`@dependabot ignore `" + ` will close this group update PR and stop Dependabot creating any more for the specific dependency (unless you unignore this specific dependency or upgrade to it yourself)
- ` + "`@dependabot unignore `" + ` will remove all of the ignore conditions of the specified dependency
- ` + "`@dependabot unignore  `" + ` will remove the ignore condition of the specified dependency and ignore conditions
`

	expected := "Some description of the PR.\n\nMore details here."

	out := removeDependabotTrailingCommand(input)
	if out != expected {
		t.Fatalf("unexpected output:\nGot:\n%q\nWant:\n%q", out, expected)
	}

	// Also test variant where the dependabot block starts with @dependabot
	input2 := "Line1\nLine2\n@dependabot rebase\n- `@dependabot rebase`\n"
	want2 := "Line1\nLine2"
	if got := removeDependabotTrailingCommand(input2); got != want2 {
		t.Fatalf("variant 2 failed: got %q want %q", got, want2)
	}

	// No block: should return trimmed trailing whitespace
	input3 := "Just a body with trailing whitespace\n\n"
	if got := removeDependabotTrailingCommand(input3); got != "Just a body with trailing whitespace" {
		t.Fatalf("no-block variant failed: got %q", got)
	}
}
