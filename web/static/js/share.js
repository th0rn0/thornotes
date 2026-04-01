const raw = document.getElementById('note-raw').textContent;
document.getElementById('content').innerHTML = DOMPurify.sanitize(marked.parse(raw));
document.querySelectorAll('pre code').forEach(el => hljs.highlightElement(el));
