# Taffy

Taffy clones a Git repository and deploys it through `flynn-receiver` (the same
builder gitreceive uses for `git push`). Dashboard GitHub deploys and
`flynn github:deploy` start a taffy job with an HTTPS clone URL; GitHub App
installations use `x-access-token` credentials.
