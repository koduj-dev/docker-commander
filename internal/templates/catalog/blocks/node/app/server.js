const http = require("http");
const port = process.env.PORT || 3000;
http.createServer((_, res) => res.end("Hello from Node 👋\n")).listen(port);
