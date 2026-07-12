package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2/formatters/html"

	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

type Blog struct {
	Title string
	Date  time.Time
	Slug  string
	HTML  template.HTML
}

type IndexData struct {
	Blogs     []Blog
	BuildTime string
}

func main() {
	buildDir := "build"

	must(os.RemoveAll(buildDir))
	must(os.MkdirAll(buildDir, 0755))
	must(syncAttachments(buildDir))

	blogs := loadBlogs()

	// Render blogs before the index so existing blogs are included.
	renderBlogs(buildDir, blogs)
	renderIndex(buildDir, blogs)
	renderRSS(buildDir, blogs)
}

var mermaidRe = regexp.MustCompile("(?s)```mermaid\n(.*?)```[ \t]*")

func preprocessMermaid(src []byte) []byte {
	return mermaidRe.ReplaceAllFunc(src, func(match []byte) []byte {
		content := string(match)
		content = content[10:] // strip "```mermaid\n"
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)

		var buf bytes.Buffer
		buf.WriteString("<pre class=\"mermaid\">")
		template.HTMLEscape(&buf, []byte(content))
		buf.WriteString("</pre>")
		return buf.Bytes()
	})
}

func loadBlogs() []Blog {
	var blogs []Blog

	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(
				highlighting.WithStyle("native"),
				highlighting.WithGuessLanguage(true),
				highlighting.WithFormatOptions(
					html.WithLineNumbers(true),
				),
			),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			gmhtml.WithUnsafe(),
		),
	)

	filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}

		base := filepath.Base(path)
		parts := strings.SplitN(base, "-", 4)
		if len(parts) < 4 {
			log.Fatalf("invalid filename format: %s", base)
		}

		dateStr := strings.Join(parts[:3], "-")
		date, err := time.Parse("2006-01-02", dateStr)
		must(err)

		slug := strings.TrimSuffix(parts[3], ".md")

		src, err := os.ReadFile(path)
		must(err)

		src = preprocessMermaid(src)

		blogs = append(blogs, Blog{
			Title: extractTitle(md, src),
			Date:  date,
			Slug:  slug,
			HTML:  template.HTML(extractBody(md, src)),
		})

		return nil
	})

	sort.Slice(blogs, func(i, j int) bool {
		return blogs[i].Date.After(blogs[j].Date)
	})

	return blogs
}

func renderBlogs(outDir string, blogs []Blog) {
	tmpl := template.Must(template.ParseFiles("blog.gohtml"))

	for _, b := range blogs {
		out := filepath.Join(outDir, b.Slug+".html")
		f := mustCreate(out)
		must(tmpl.Execute(f, b))
		f.Close()
	}
}

func renderIndex(outDir string, blogs []Blog) {
	tmpl := template.Must(template.ParseFiles("index.gohtml"))

	out := filepath.Join(outDir, "index.html")
	f := mustCreate(out)
	must(tmpl.Execute(f, IndexData{
		Blogs:     blogs,
		BuildTime: "GMT+5:30",
	}))
	f.Close()
}

func renderRSS(outDir string, blogs []Blog) {
	rssPath := filepath.Join(outDir, "rss.xml")
	f := mustCreate(rssPath)
	defer f.Close()

	fmt.Fprint(f, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom">
  <channel>
    <title>Melonlorrd's Blog</title>
    <link>https://melonlorrd.github.io</link>
    <description>Infrastructure and Systems Engineering</description>
    <language>en-us</language>
    <atom:link href="https://melonlorrd.github.io/rss.xml" rel="self" type="application/rss+xml"/>
`)

	for _, b := range blogs {
		fmt.Fprintf(f, `    <item>
      <title>`)
		template.HTMLEscape(f, []byte(b.Title))
		fmt.Fprintf(f, `</title>
      <link>https://melonlorrd.github.io/`)
		template.HTMLEscape(f, []byte(b.Slug))
		fmt.Fprintf(f, `.html</link>
      <guid>https://melonlorrd.github.io/`)
		template.HTMLEscape(f, []byte(b.Slug))
		fmt.Fprintf(f, `.html</guid>
      <pubDate>`)
		fmt.Fprint(f, b.Date.Format(time.RFC1123Z))
		fmt.Fprint(f, `</pubDate>
      <description><![CDATA[`)
		template.HTMLEscape(f, []byte(b.HTML))
		fmt.Fprintf(f, `]]></description>
    </item>
`)
	}

	fmt.Fprint(f, `  </channel>
</rss>`)
}

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
		if err != nil || d.IsDir() || !isImage(path) {
			return err
		}

		dst := filepath.Join(outDir, filepath.Base(path))

		srcAbs, _ := filepath.Abs(path)
		dstAbs, _ := filepath.Abs(dst)
		if srcAbs == dstAbs {
			return nil
		}

		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()

		dstFile, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, src)
		return err
	})
}

func isImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	}
	return false
}

func extractTitle(md goldmark.Markdown, src []byte) string {
	reader := text.NewReader(src)
	doc := md.Parser().Parse(reader)

	var title string

	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		h, ok := n.(*ast.Heading)
		if !ok || h.Level != 1 {
			return ast.WalkContinue, nil
		}

		var buf bytes.Buffer
		for c := h.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				buf.Write(t.Segment.Value(src))
			}
		}

		title = strings.TrimSpace(buf.String())

		return ast.WalkStop, nil
	})

	return title
}

func extractBody(md goldmark.Markdown, src []byte) string {
	reader := text.NewReader(src)
	doc := md.Parser().Parse(reader)

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
			continue
		}
	}

	return strings.TrimSpace(buf.String())
}
