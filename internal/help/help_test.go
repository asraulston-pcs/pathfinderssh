// internal/help/help_test.go
package help

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEveryTopicHasContentAndEveryContentFileIsATopic(t *testing.T) {
	// Both directions. A topic with no file is a broken deep link; a file
	// with no topic is documentation nobody can reach, which is worse
	// because it looks like it shipped.
	declared := map[string]bool{}
	for _, tp := range topics {
		if _, err := readContent(tp.File); err != nil {
			t.Errorf("topic %s: %v", tp.ID, err)
		}
		declared[tp.File] = true
	}
	for _, f := range ContentFiles() {
		if !declared[f] {
			t.Errorf("content/%s is not referenced by any topic", f)
		}
	}
}

func TestTopicIDsAreUniqueAndAnchorSafe(t *testing.T) {
	ok := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	seen := map[Topic]bool{}
	for _, tp := range topics {
		if seen[tp.ID] {
			t.Errorf("duplicate topic id %q", tp.ID)
		}
		seen[tp.ID] = true
		if !ok.MatchString(string(tp.ID)) {
			t.Errorf("topic id %q is not anchor-safe", tp.ID)
		}
		if tp.Title == "" {
			t.Errorf("topic %q has no title", tp.ID)
		}
	}
}

func TestKnownAgreesWithTopics(t *testing.T) {
	for _, id := range Topics() {
		if !Known(id) {
			t.Errorf("Topics() returned %q but Known says no", id)
		}
	}
	if Known("no-such-topic") {
		t.Error("Known accepted a topic that does not exist")
	}
}

func TestRenderResolvesEveryImage(t *testing.T) {
	// This is the test that catches a renamed or deleted screenshot. The
	// renderer only warns, on purpose -- a missing picture must not stop
	// help opening for a user -- so the enforcement has to live here.
	_, warnings, err := Render("test")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, w := range warnings {
		t.Errorf("unresolved image: %s", w)
	}
}

func TestEveryEmbeddedImageIsUsed(t *testing.T) {
	page, _, err := Render("test")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// An image that is embedded but never referenced is dead weight in
	// every binary shipped. Each image appears in the page as its own
	// base64 blob, so presence is checked by reading the source bytes back
	// out of the content FS and looking for their encoding.
	for _, img := range Images() {
		uri, err := dataURI(img)
		if err != nil {
			t.Errorf("%s: %v", img, err)
			continue
		}
		if !strings.Contains(string(page), uri) {
			t.Errorf("%s is embedded but never referenced by any topic", img)
		}
	}
}

func TestThePageHasNoExternalReferences(t *testing.T) {
	// The whole distribution story rests on this: one file, openable from
	// anywhere, with nothing to fetch. A single relative src works on the
	// machine that rendered it and fails on every other one.
	page, _, err := Render("v0.1")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(page)

	for _, bad := range []string{"<link ", "@import", "<script"} {
		if strings.Contains(body, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	// Every src= must be a data: URI. Anything else is a fetch.
	for _, m := range regexp.MustCompile(`src="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		if !strings.HasPrefix(m[1], "data:") {
			t.Errorf("non-inline src: %q", m[1])
		}
	}
}

func TestEveryTopicRendersItsSectionAnchor(t *testing.T) {
	page, _, err := Render("v0.1")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, id := range Topics() {
		if !strings.Contains(string(page), `<section id="`+string(id)+`"`) {
			t.Errorf("no section anchor for topic %q", id)
		}
	}
}

func TestInPageLinksPointAtSomethingReal(t *testing.T) {
	// A cross-reference between topics is written by hand and is exactly
	// the kind of thing that rots when a heading is reworded.
	page, _, err := Render("v0.1")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(page)
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		ids[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(body, -1) {
		if !ids[m[1]] {
			t.Errorf("link to #%s but nothing has that id", m[1])
		}
	}
}

func TestHeadingIDsDoNotCollideAcrossTopics(t *testing.T) {
	page, _, err := Render("v0.1")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	seen := map[string]int{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(string(page), -1) {
		seen[m[1]]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("id %q appears %d times", id, n)
		}
	}
}

func TestEnsureWritesOnceThenReusesTheStampedFile(t *testing.T) {
	dir := t.TempDir()

	path, err := Ensure(dir, "v1.2.3")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	st1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := stampOf(path); got != "v1.2.3" {
		t.Fatalf("stamp = %q, want v1.2.3", got)
	}

	// Mark the file so a rewrite is detectable regardless of timestamp
	// resolution, which is coarse on some filesystems.
	marker := []byte("<!-- pathfinderssh-help v1.2.3 -->\nMARKED")
	if err := os.WriteFile(path, marker, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Ensure(dir, "v1.2.3"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(marker) {
		t.Error("same version re-rendered; the stamp check did not hold")
	}

	// A different version must rewrite.
	if _, err := Ensure(dir, "v1.3.0"); err != nil {
		t.Fatalf("third ensure: %v", err)
	}
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(marker) {
		t.Error("new version did not re-render")
	}
	if st1.Size() == 0 {
		t.Error("first render wrote nothing")
	}
}

func TestEnsureLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Ensure(dir, "v1"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left behind %s", e.Name())
		}
	}
}

func TestEnsureRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := Ensure("", "v1"); err == nil {
		t.Error("expected an error for an empty directory")
	}
}

func TestStampOfAForeignFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	if err := os.WriteFile(p, []byte("<html>somebody else's file</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stampOf(p); got != "" {
		t.Errorf("stamp = %q, want empty", got)
	}
	// And Ensure must overwrite it rather than trusting it.
	if _, err := Ensure(dir, "v1"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := stampOf(p); got != "v1" {
		t.Errorf("stamp after ensure = %q, want v1", got)
	}
}

func TestURLIsAThreeSlashFileURLWithAFragment(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)

	u, err := URL(p, TopicCapture)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	got := u.String()
	if !strings.HasPrefix(got, "file:///") {
		t.Errorf("%q does not start with file:///", got)
	}
	if u.Host != "" {
		t.Errorf("host = %q, want empty; a drive letter must not become a host", u.Host)
	}
	if !strings.HasSuffix(got, "#capture") {
		t.Errorf("%q has no fragment", got)
	}
	if strings.Contains(got, `\`) {
		t.Errorf("%q contains a backslash", got)
	}
}

func TestAWindowsDriveLetterLandsInThePathNotTheHost(t *testing.T) {
	// The bug this guards against is file://C:/Users/... , where C: is
	// parsed as the HOST and the browser opens nothing. It cannot be
	// caught by running the tests on Linux unless the shape is asserted
	// directly, because filepath.Abs is the only platform-dependent part
	// and it is not the part that gets this wrong.
	u := fileURL("C:/Users/you/.pathfinderssh/help/help.html")
	if u.Host != "" {
		t.Errorf("host = %q, want empty", u.Host)
	}
	if got := u.String(); got != "file:///C:/Users/you/.pathfinderssh/help/help.html" {
		t.Errorf("got %q", got)
	}

	// A unix path must not grow a second leading slash.
	if got := fileURL("/home/you/.pathfinderssh/help/help.html").String(); got !=
		"file:///home/you/.pathfinderssh/help/help.html" {
		t.Errorf("got %q", got)
	}
}

func TestURLRejectsAnEmptyPath(t *testing.T) {
	if _, err := URL("", TopicCrawl); err == nil {
		t.Error("expected an error for an empty path")
	}
}

func TestNoTopicIsEmpty(t *testing.T) {
	// A stub file that got embedded and forgotten renders as a heading
	// with nothing under it, which reads as a bug in the program rather
	// than a gap in the documentation.
	for _, tp := range topics {
		b, err := readContent(tp.File)
		if err != nil {
			t.Fatalf("%s: %v", tp.ID, err)
		}
		if len(b) < 400 {
			t.Errorf("topic %s is %d bytes; that is a stub", tp.ID, len(b))
		}
	}
}
