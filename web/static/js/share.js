const raw = document.getElementById('note-raw').textContent;
// Render [[wikilinks]] as plain text on share pages (no navigation context).
const processed = raw.replace(/\[\[([^\]\n]+)\]\]/g, '$1');
document.getElementById('content').innerHTML = DOMPurify.sanitize(marked.parse(processed));
document.querySelectorAll('pre code').forEach(el => hljs.highlightElement(el));
