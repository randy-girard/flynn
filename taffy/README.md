# Taffy

Taffy clones a Git repository and deploys it through `flynn-receiver` (the same
builder gitreceive uses for `git push`). Dashboard GitHub deploys and
`flynn github:deploy` start a taffy job with an HTTPS clone URL; GitHub App
installations use `x-access-token` credentials.

flynn-receiver takes the app's build lease in the controller before it builds.
A taffy deploy started while a `git push` or another deploy of the app is
building fails with `a git push to this app is in progress` and builds
nothing (see [Apps](../docs/content/apps.md#one-build-at-a-time)).
