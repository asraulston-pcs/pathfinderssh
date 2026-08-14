// internal/help/render.go
//
// Markdown in, one self-contained HTML page out.
//
// # The image rewrite is the whole trick
//
// goldmark parses ![](images/crawl.png) into an ast.Image whose Destination is
// a relative path. An AST transformer replaces that destination with a data:
// URI carrying the bytes from the embedded FS, so the rendered page carries
// its own pictures and can be opened from anywhere.
//
// An image that does not resolve is a WARNING, not an error. The alt text is
// left in place and the build still produces a page. A help system that
// refuses to open because a screenshot was renamed is worse than a help system
// with a missing screenshot -- and the test suite catches the rename anyway,
// which is where that should be caught.
//
// # Why each topic is rendered separately
//
// One document per file, concatenated into sections. That keeps the anchor for
// a topic under this package's control rather than derived from whatever the
// first heading happens to say, and it means a topic can be reordered without
// changing any link. Heading ids are prefixed per topic so two files may both
// have a "Fields" heading without colliding.
package help

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Render builds the whole page. version is stamped into the first line and
// shown in the footer; it may be empty.
//
// Warnings name every image reference that did not resolve. They are returned
// rather than logged so a test can assert there are none, which is the only
// place a broken screenshot link is cheap to find.
func Render(version string) (page []byte, warnings []string, err error) {
	var body bytes.Buffer

	for _, t := range topics {
		src, err := readContent(t.File)
		if err != nil {
			return nil, nil, fmt.Errorf("help: topic %s: %w", t.ID, err)
		}

		tr := &imageInliner{prefix: string(t.ID)}
		md := newMarkdown(tr)

		// Heading ids are namespaced per topic through the parse
		// context, which is where goldmark takes an IDs generator.
		ctx := parser.NewContext(parser.WithIDs(&prefixedIDs{
			prefix: string(t.ID),
			seen:   map[string]bool{},
		}))

		var out bytes.Buffer
		if err := md.Convert(src, &out, parser.WithContext(ctx)); err != nil {
			return nil, nil, fmt.Errorf("help: topic %s: %w", t.ID, err)
		}
		warnings = append(warnings, tr.warnings...)

		fmt.Fprintf(&body, "<section id=%q class=\"topic\">\n", t.ID)
		body.Write(out.Bytes())
		body.WriteString("</section>\n")
	}

	var nav bytes.Buffer
	for _, t := range topics {
		fmt.Fprintf(&nav, "<li><a href=\"#%s\">%s</a></li>\n", t.ID, html.EscapeString(t.Title))
	}

	var doc bytes.Buffer
	fmt.Fprintf(&doc, "%s%s -->\n", stampPrefix, version)
	fmt.Fprintf(&doc, pageTemplate, pageCSS, nav.String(), body.String(), html.EscapeString(footerText(version)))
	return doc.Bytes(), warnings, nil
}

// footerText is the one line at the bottom of the page.
func footerText(version string) string {
	if version == "" {
		return "PathfinderSSH"
	}
	return "PathfinderSSH " + version
}

// newMarkdown builds a parser for one topic.
//
// GFM is on for tables: the field references are tables, and a field reference
// written as a bullet list is harder to scan than the dialog it describes.
// Raw HTML stays DISABLED -- the content is ours, but leaving the unsafe door
// shut means a future contributor cannot paste a <script> into a page that is
// opened from file:// with no origin.
func newMarkdown(tr parser.ASTTransformer) goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(tr, 100)),
		),
	)
}

// prefixedIDs generates heading anchors namespaced to one topic.
//
// goldmark's own generator dedupes within a document, and each topic is its
// own document, so without this two topics that both say "## Fields" produce
// two elements with id="fields" and the second is unreachable.
type prefixedIDs struct {
	prefix string
	seen   map[string]bool
}

func (p *prefixedIDs) Generate(value []byte, kind ast.NodeKind) []byte {
	slug := slugify(string(value))
	if slug == "" {
		slug = "section"
	}
	id := p.prefix + "-" + slug
	for n := 2; p.seen[id]; n++ {
		id = fmt.Sprintf("%s-%s-%d", p.prefix, slug, n)
	}
	p.seen[id] = true
	return []byte(id)
}

func (p *prefixedIDs) Put(value []byte) { p.seen[string(value)] = true }

// slugify reduces heading text to an anchor-safe token.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// imageInliner rewrites relative image destinations to data: URIs.
type imageInliner struct {
	prefix   string
	warnings []string
}

func (t *imageInliner) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		img, ok := n.(*ast.Image)
		if !ok {
			return ast.WalkContinue, nil
		}
		dest := string(img.Destination)
		if dest == "" || strings.Contains(dest, "://") || strings.HasPrefix(dest, "data:") {
			return ast.WalkContinue, nil
		}
		uri, err := dataURI(dest)
		if err != nil {
			t.warnings = append(t.warnings,
				fmt.Sprintf("%s: image %q: %v", t.prefix, dest, err))
			return ast.WalkContinue, nil
		}
		img.Destination = []byte(uri)
		return ast.WalkContinue, nil
	})
}

// dataURI reads one image out of the embedded content and encodes it.
func dataURI(dest string) (string, error) {
	clean := path.Clean(dest)
	if strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
		return "", fmt.Errorf("outside the content directory")
	}
	data, err := fs.ReadFile(contentFS, contentDir+"/"+clean)
	if err != nil {
		return "", err
	}
	mt, ok := imageMIME[strings.ToLower(path.Ext(clean))]
	if !ok {
		return "", fmt.Errorf("unsupported image type %q", path.Ext(clean))
	}
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// imageMIME is an allowlist rather than a lookup: mime.TypeByExtension reads
// the host's /etc/mime.types, so the same source would produce a different
// page on a different machine.
var imageMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
}

// Images lists every image file in the embedded content, for tests.
func Images() []string {
	var out []string
	_ = fs.WalkDir(contentFS, contentDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if _, ok := imageMIME[strings.ToLower(path.Ext(p))]; ok {
			out = append(out, strings.TrimPrefix(p, contentDir+"/"))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// ContentFiles lists every markdown file in the embedded content, for tests.
func ContentFiles() []string {
	var out []string
	entries, err := fs.ReadDir(contentFS, contentDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
