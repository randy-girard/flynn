require "socket"
require "sinatra"

$stdout.sync = true
set :bind, "0.0.0.0"

get "/" do
  "Hello from Flynn on port #{ENV["PORT"]} from container #{Socket.gethostname}\n"
end
