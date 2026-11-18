package main

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	gohtml "html"
	"html/template"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const (
	baseURL   = "https://melonlorrd.github.io"
	siteTitle = "Melonlorrd's Blog"
	siteDesc  = "Infrastructure and Systems Engineering"
	buildDir  = "build"
)

type Blog struct {
	Title       string
	Date        time.Time
	Slug        string
	Summary     string
	ReadingTime int
	HTML        template.HTML // for the website
	FeedHTML    string        // for RSS readers: no highlighting markup, absolute URLs
}

type PostPage struct {
	Blog
	BaseURL string
	Newer   *Blog
	Older   *Blog
}

type YearGroup struct {
	Year  int
	Blogs []Blog
}

type IndexData struct {
	Years     []YearGroup
	BuildDate string
	BuildTZ   string
}

func main() {
	must(os.RemoveAll(buildDir))
	must(os.MkdirAll(buildDir, 0755))
	must(syncAttachments(buildDir))
	must(writeStylesheet(buildDir))

	blogs := loadBlogs()

	renderBlogs(buildDir, blogs)
	renderIndex(buildDir, blogs)
	renderRSS(buildDir, blogs)
}

// ---------------------------------------------------------------------------
// Markdown
// ---------------------------------------------------------------------------

var mermaidRe = regexp.MustCompile("(?s)```mermaid\n(.*?)```[ \t]*")

func preprocessMermaid(src []byte) []byte {
	return mermaidRe.ReplaceAllFunc(src, func(match []byte) []byte {
		content := string(match)
		content = content[len("```mermaid\n"):]
		content = strings.TrimSpace(content)
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)

		var buf bytes.Buffer
		buf.WriteString("<pre class=\"mermaid\">")
		template.HTMLEscape(&buf, []byte(content))
		buf.WriteString("</pre>")
		return buf.Bytes()
	})
}

// imageAttrs adds lazy loading and intrinsic width/height to local images so
// the page doesn't jump around while they load.
type imageAttrs struct{}

func (imageAttrs) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		img, ok := n.(*ast.Image)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		img.SetAttributeString("loading", []byte("lazy"))
		img.SetAttributeString("decoding", []byte("async"))
		if w, h, ok := imageSize(string(img.Destination)); ok {
			img.SetAttributeString("width", []byte(strconv.Itoa(w)))
			img.SetAttributeString("height", []byte(strconv.Itoa(h)))
		}
		return ast.WalkContinue, nil
	})
}

func imageSize(dest string) (int, int, bool) {
	if strings.Contains(dest, "://") || strings.HasPrefix(dest, "data:") {
		return 0, 0, false
	}
	f, err := os.Open(filepath.Clean(strings.TrimPrefix(dest, "/")))
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func newMarkdown(highlight bool) goldmark.Markdown {
	exts := []goldmark.Extender{extension.GFM}
	if highlight {
		exts = append(exts, highlighting.NewHighlighting(
			highlighting.WithGuessLanguage(true),
			// Classes instead of inline styles so the colours can follow the
			// light/dark theme (see writeStylesheet).
			highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
		))
	}
	return goldmark.New(
		goldmark.WithExtensions(exts...),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(imageAttrs{}, 500)),
		),
		goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
	)
}

// A paragraph holding only an image becomes a <figure>, with alt as caption.
var loneImageRe = regexp.MustCompile(`<p>(<img [^>]*alt="([^"]+)"[^>]*>)</p>`)

func figures(s string) string {
	return loneImageRe.ReplaceAllString(s, "<figure>$1<figcaption>$2</figcaption></figure>")
}

// Clickable "#" anchors on headings, website only.
var headingRe = regexp.MustCompile(`(?s)<h([2-4]) id="([^"]+)">(.*?)</h[2-4]>`)

func headingAnchors(s string) string {
	return headingRe.ReplaceAllString(s, `<h$1 id="$2">$3<a class="anchor" href="#$2" aria-label="Link to this section">#</a></h$1>`)
}

func loadBlogs() []Blog {
	var blogs []Blog

	webMD := newMarkdown(true)
	feedMD := newMarkdown(false)

	entries, err := os.ReadDir(".")
	must(err)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}

		base := e.Name()
		parts := strings.SplitN(base, "-", 4)
		if len(parts) < 4 {
			log.Fatalf("invalid filename format: %s", base)
		}

		date, err := time.Parse("2006-01-02", strings.Join(parts[:3], "-"))
		must(err)

		src, err := os.ReadFile(base)
		must(err)

		words := len(strings.Fields(string(src)))
		src = preprocessMermaid(src)

		title, summary := extractMeta(webMD, src)

		webHTML := headingAnchors(figures(renderBody(webMD, src)))
		feedHTML := figures(renderBody(feedMD, src))
		feedHTML = makeAbsoluteURLs(convertMermaidForRSS(feedHTML), baseURL)

		blogs = append(blogs, Blog{
			Title:       title,
			Date:        date,
			Slug:        strings.TrimSuffix(parts[3], ".md"),
			Summary:     summary,
			ReadingTime: max(1, (words+110)/220),
			HTML:        template.HTML(webHTML),
			FeedHTML:    feedHTML,
		})
	}

	sort.Slice(blogs, func(i, j int) bool {
		return blogs[i].Date.After(blogs[j].Date)
	})

	return blogs
}

// nodeText collects the plain text of a node, including inline code.
func nodeText(n ast.Node, src []byte, buf *bytes.Buffer) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			buf.Write(t.Segment.Value(src))
			if t.SoftLineBreak() || t.HardLineBreak() {
				buf.WriteByte(' ')
			}
		case *ast.String:
			buf.Write(t.Value)
		default:
			nodeText(c, src, buf)
		}
	}
}

// extractMeta returns the first H1 and a short plain-text summary taken from
// the first paragraph.
func extractMeta(md goldmark.Markdown, src []byte) (title, summary string) {
	doc := md.Parser().Parse(text.NewReader(src))

	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		var buf bytes.Buffer
		switch v := n.(type) {
		case *ast.Heading:
			if v.Level == 1 && title == "" {
				nodeText(v, src, &buf)
				title = strings.TrimSpace(buf.String())
			}
		case *ast.Paragraph:
			if summary == "" {
				nodeText(v, src, &buf)
				summary = truncate(strings.Join(strings.Fields(buf.String()), " "), 200)
			}
		}
		if title != "" && summary != "" {
			break
		}
	}
	return title, summary
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

func renderBody(md goldmark.Markdown, src []byte) string {
	doc := md.Parser().Parse(text.NewReader(src))

	var buf bytes.Buffer
	skipFirstH1 := true

	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if skipFirstH1 {
			if h, ok := n.(*ast.Heading); ok && h.Level == 1 {
				skipFirstH1 = false
				continue
			}
		}
		if err := md.Renderer().Render(&buf, src, n); err != nil {
			log.Printf("render: %v", err)
		}
	}

	return strings.TrimSpace(buf.String())
}

// ---------------------------------------------------------------------------
// Pages
// ---------------------------------------------------------------------------

func renderBlogs(outDir string, blogs []Blog) {
	tmpl := template.Must(template.ParseFiles("blog.gohtml"))

	for i, b := range blogs {
		page := PostPage{Blog: b, BaseURL: baseURL}
		if i > 0 {
			page.Newer = &blogs[i-1]
		}
		if i < len(blogs)-1 {
			page.Older = &blogs[i+1]
		}

		f := mustCreate(filepath.Join(outDir, b.Slug+".html"))
		must(tmpl.Execute(f, page))
		f.Close()
	}
}

func renderIndex(outDir string, blogs []Blog) {
	tmpl := template.Must(template.ParseFiles("index.gohtml"))

	var years []YearGroup
	for _, b := range blogs {
		y := b.Date.Year()
		if len(years) == 0 || years[len(years)-1].Year != y {
			years = append(years, YearGroup{Year: y})
		}
		years[len(years)-1].Blogs = append(years[len(years)-1].Blogs, b)
	}

	ist := time.FixedZone("IST", 5*3600+30*60)

	f := mustCreate(filepath.Join(outDir, "index.html"))
	must(tmpl.Execute(f, IndexData{
		Years:     years,
		BuildDate: time.Now().In(ist).Format("2006.01.02"),
		BuildTZ:   "GMT+5:30",
	}))
	f.Close()
}

// writeStylesheet writes style.css plus syntax colours for light and dark.
func writeStylesheet(outDir string) error {
	css, err := os.ReadFile("style.css")
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	buf.Write(css)

	formatter := chromahtml.New(chromahtml.WithClasses(true))

	buf.WriteString("\n/* Syntax highlighting: light */\n")
	if err := formatter.WriteCSS(&buf, styles.Get("github")); err != nil {
		return err
	}
	buf.WriteString("\n/* Syntax highlighting: dark */\n@media (prefers-color-scheme: dark) {\n")
	if err := formatter.WriteCSS(&buf, styles.Get("github-dark")); err != nil {
		return err
	}
	buf.WriteString("}\n")

	// Keep our own code background rather than the theme's.
	buf.WriteString(".prose pre.chroma { background-color: var(--code-bg); color: var(--text); }\n")

	return os.WriteFile(filepath.Join(outDir, "style.css"), buf.Bytes(), 0644)
}

// ---------------------------------------------------------------------------
// RSS
// ---------------------------------------------------------------------------

var mermaidPreRe = regexp.MustCompile(`(?s)<pre class="mermaid">(.*?)</pre>`)

// RSS readers don't run JavaScript, so diagrams become images from mermaid.ink.
func convertMermaidForRSS(htmlContent string) string {
	return mermaidPreRe.ReplaceAllStringFunc(htmlContent, func(match string) string {
		sub := mermaidPreRe.FindStringSubmatch(match)
		raw := strings.TrimSpace(gohtml.UnescapeString(sub[1]))

		// URL-safe base64: standard base64 contains "/" and "+", which
		// mermaid.ink treats as path separators and answers with a 404.
		encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
		// PNG rather than SVG: some readers and image proxies drop SVGs.
		imgURL := "https://mermaid.ink/img/" + encoded + "?type=png&amp;bgColor=!white"

		return fmt.Sprintf(`<figure><img src="%s" alt="Diagram" /></figure>`, imgURL)
	})
}

func renderRSS(outDir string, blogs []Blog) {
	f := mustCreate(filepath.Join(outDir, "rss.xml"))
	defer f.Close()

	// Latest post date rather than time.Now(), so rebuilding without new
	// posts doesn't make readers think the feed changed.
	lastBuildDate := time.Now().Format(time.RFC1123Z)
	if len(blogs) > 0 {
		lastBuildDate = blogs[0].Date.Format(time.RFC1123Z)
	}

	fmt.Fprintf(f, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:content="http://purl.org/rss/1.0/modules/content/">
  <channel>
    <title>%s</title>
    <link>%s/</link>
    <description>%s</description>
    <language>en-us</language>
    <lastBuildDate>%s</lastBuildDate>
    <atom:link href="%s/rss.xml" rel="self" type="application/rss+xml"/>
    <image>
      <url>%s/favicon.png</url>
      <title>%s</title>
      <link>%s/</link>
    </image>
`, xmlEscape(siteTitle), baseURL, xmlEscape(siteDesc), lastBuildDate, baseURL, baseURL, xmlEscape(siteTitle), baseURL)

	for _, b := range blogs {
		link := fmt.Sprintf("%s/%s.html", baseURL, b.Slug)
		fmt.Fprintf(f, `    <item>
      <title>%s</title>
      <link>%s</link>
      <guid isPermaLink="true">%s</guid>
      <pubDate>%s</pubDate>
      <description>%s</description>
      <content:encoded><![CDATA[%s]]></content:encoded>
    </item>
`, xmlEscape(b.Title), link, link, b.Date.Format(time.RFC1123Z), xmlEscape(b.Summary), safeCDATA(b.FeedHTML))
	}

	fmt.Fprint(f, "  </channel>\n</rss>\n")
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	must(xml.EscapeText(&buf, []byte(s)))
	return buf.String()
}

var urlAttrRe = regexp.MustCompile(`(?i)\b(src|href)=["']([^"']+)["']`)

func makeAbsoluteURLs(htmlStr string, baseURL string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return urlAttrRe.ReplaceAllStringFunc(htmlStr, func(match string) string {
		sub := urlAttrRe.FindStringSubmatch(match)
		attr, val := sub[1], sub[2]

		lower := strings.ToLower(val)
		if strings.HasPrefix(lower, "http://") ||
			strings.HasPrefix(lower, "https://") ||
			strings.HasPrefix(lower, "mailto:") ||
			strings.HasPrefix(lower, "data:") ||
			strings.HasPrefix(lower, "#") {
			return match
		}

		return fmt.Sprintf(`%s="%s/%s"`, attr, baseURL, strings.TrimPrefix(val, "/"))
	})
}

func safeCDATA(s string) string {
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}

// ---------------------------------------------------------------------------
// Files
// ---------------------------------------------------------------------------

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func mustCreate(path string) *os.File {
	f, err := os.Create(path)
	must(err)
	return f
}

func syncAttachments(outDir string) error {
	return filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip the output dir and dot-dirs like .git.
			if path != "." && (path == outDir || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isImage(path) {
			return nil
		}
		return copyFile(path, filepath.Join(outDir, filepath.Base(path)))
	})
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func isImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg":
		return true
	}
	return false
}
