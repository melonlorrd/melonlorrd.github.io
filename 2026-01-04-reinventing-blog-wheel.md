# Reinventing the Blog Wheel, and Happy New Year 2026!

I reinvented the blog wheel. Again. At this point, I think I’ve spent more time changing how I write blogs than actually writing them. The tooling has become the hobby. I went from Jekyll to Astro, back to Jekyll, then to Docusaurus, and eventually decided that all of this is so hard for the simple blog I need. I know I know pandoc exists. But still, I did the most reasonable thing: I built my own blog generator.

The current setup is a small Go program that does one thing reasonably (lmao), turn Markdown files into static HTML files, i.e. a website. I’m using the Goldmark markdown parser to process posts, with Chroma handling syntax highlighting for code blocks. For layout, I’m using Go’s templating system for both the blog pages and the main site. There’s a home page template for well, the home page and blog template for blogs. Deployment is simple, GitHub Actions runs the main.go on every push, builds the site converting Markdown to static HTML files, and publishes it to GitHub Pages.

Is the code good? Not really. It’s inefficient, poorly structured in places, and very much held together by "this works, don’t touch it." But I had fun building it, and that’s kind of the whole point. There’s something satisfying about owning the entire pipeline, even if it’s a little messy.

Anyway, Happy New Year 2026. I plan to write more about the side projects I keep starting, breaking, and rebuilding this year. No promises, but at least the blog is ready.