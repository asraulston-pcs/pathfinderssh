// internal/help/page.go
//
// The shell around the rendered topics: the template and its stylesheet.
//
// Both are constants in Go rather than files in content/ because they are not
// documentation. Nobody edits the stylesheet to fix a wrong sentence, and
// keeping them here means the content directory holds exactly what an author
// touches.
//
// # Why the CSS is inline
//
// A <link> to a stylesheet is an external reference, and the point of the
// whole design is that the page has none. Same reason there is no web font:
// the font stack names what is already installed.
package help

// pageTemplate takes: the stylesheet, the sidebar list items, the body, and
// the footer line.
const pageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PathfinderSSH Help</title>
<style>
%s
</style>
</head>
<body>
<nav id="toc">
<div class="brand">PathfinderSSH</div>
<ul>
%s</ul>
</nav>
<main>
%s
<footer>%s</footer>
</main>
</body>
</html>
`

// pageCSS is deliberately small. It sets a reading column, a sidebar, and
// enough table and code styling to make a field reference scannable.
const pageCSS = `
:root {
  --bg: #ffffff;
  --fg: #1c1f24;
  --muted: #5c6470;
  --rule: #dfe3e8;
  --accent: #1a6fd4;
  --code-bg: #f4f6f8;
  --side-bg: #f7f9fb;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #14171c;
    --fg: #e6e9ee;
    --muted: #9aa3b0;
    --rule: #2b313a;
    --accent: #6fb0ff;
    --code-bg: #1c2129;
    --side-bg: #171b21;
  }
}
* { box-sizing: border-box; }
html { scroll-behavior: smooth; }
body {
  margin: 0;
  background: var(--bg);
  color: var(--fg);
  font: 16px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto,
        "Helvetica Neue", Arial, sans-serif;
}
#toc {
  position: fixed;
  top: 0; left: 0; bottom: 0;
  width: 250px;
  padding: 24px 18px;
  overflow-y: auto;
  background: var(--side-bg);
  border-right: 1px solid var(--rule);
}
#toc .brand {
  font-weight: 700;
  letter-spacing: .02em;
  margin-bottom: 16px;
}
#toc ul { list-style: none; margin: 0; padding: 0; }
#toc li { margin: 2px 0; }
#toc a {
  display: block;
  padding: 6px 10px;
  border-radius: 6px;
  color: var(--fg);
  text-decoration: none;
  font-size: 14px;
}
#toc a:hover { background: rgba(127,127,127,.14); }
main {
  margin-left: 250px;
  padding: 40px 44px 80px;
  max-width: 900px;
}
section.topic {
  padding-top: 12px;
  margin-bottom: 44px;
  scroll-margin-top: 16px;
}
section.topic + section.topic { border-top: 1px solid var(--rule); padding-top: 36px; }
h1 { font-size: 30px; margin: 0 0 .5em; letter-spacing: -.01em; }
h2 { font-size: 21px; margin: 1.8em 0 .5em; }
h3 { font-size: 17px; margin: 1.5em 0 .4em; }
p, li { color: var(--fg); }
a { color: var(--accent); }
code {
  font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas,
               "Liberation Mono", monospace;
  font-size: .9em;
  background: var(--code-bg);
  padding: .12em .35em;
  border-radius: 4px;
}
pre {
  background: var(--code-bg);
  border: 1px solid var(--rule);
  border-radius: 8px;
  padding: 12px 14px;
  overflow-x: auto;
}
pre code { background: none; padding: 0; }
table {
  border-collapse: collapse;
  width: 100%;
  margin: 1em 0;
  font-size: 15px;
}
th, td {
  border-bottom: 1px solid var(--rule);
  padding: 8px 10px;
  text-align: left;
  vertical-align: top;
}
th { font-weight: 600; }
td:first-child { white-space: nowrap; font-weight: 600; }
img {
  max-width: 100%;
  border: 1px solid var(--rule);
  border-radius: 8px;
  display: block;
  margin: 1.2em 0;
}
blockquote {
  margin: 1.2em 0;
  padding: .6em 1em;
  border-left: 3px solid var(--accent);
  background: var(--code-bg);
  border-radius: 0 6px 6px 0;
}
blockquote p { margin: .3em 0; }
footer {
  margin-top: 60px;
  padding-top: 16px;
  border-top: 1px solid var(--rule);
  color: var(--muted);
  font-size: 13px;
}
@media (max-width: 760px) {
  #toc { position: static; width: auto; border-right: 0; border-bottom: 1px solid var(--rule); }
  main { margin-left: 0; padding: 24px 18px 60px; }
}
`
