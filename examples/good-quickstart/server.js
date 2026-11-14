import http from "node:http";

const once = process.argv.includes("--once");
const server = http.createServer((_req, res) => res.end("ok\n"));

server.listen(3000, () => {
  console.log("ready on http://localhost:3000");
  if (once) server.close();
});
