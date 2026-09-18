#!/usr/bin/env ruby
# Tiny HTTP server for the custom-buildpack smoke app. Listens on PORT.
# Body comes from .buildpack-stamp (written by bin/compile) so a 200 with
# "custom-buildpack ok" proves the custom compile ran, not only a stock pack.
require "socket"

stamp_path = File.expand_path(".buildpack-stamp", File.dirname(__FILE__))
body = begin
  File.read(stamp_path).strip
rescue Errno::ENOENT
  "custom-buildpack missing-stamp"
end
body = "custom-buildpack ok" if body.empty?
body += "\n"

port = Integer(ENV.fetch("PORT", "8080"))
server = TCPServer.new("0.0.0.0", port)
loop do
  client = server.accept
  begin
    client.gets
    client.write(
      "HTTP/1.1 200 OK\r\n" \
      "Content-Type: text/plain; charset=utf-8\r\n" \
      "Content-Length: #{body.bytesize}\r\n" \
      "Connection: close\r\n" \
      "\r\n" \
      "#{body}"
    )
  ensure
    client.close
  end
end
