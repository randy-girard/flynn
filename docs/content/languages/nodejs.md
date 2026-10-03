---
title: Node.js
layout: docs
---

# Node.js

Node.js is supported by the [Node.js buildpack](https://github.com/heroku/heroku-buildpack-nodejs).

## Detection

The Node.js buildpack is used if the repository contains a [`package.json`](https://www.npmjs.org/doc/files/package.json.html) file.

On **arm64** Flynn hosts (Apple Silicon Vagrant nodes), classic `heroku-buildpack-nodejs` still ships x86_64 helpers and linux-x64 Node. The cluster nodes register `qemu-user-static` binfmt (qemu **9+**; Ubuntu 24.04's qemu 8.2 crashes Node with `QEMU internal SIGSEGV {code=MAPERR, addr=0x20}`), and the heroku-24 slug image includes an amd64 glibc userland so those binaries can run. `git push` prints a notice when that translation is in use. Rebuild heroku-24/slugbuilder after pulling the glibc change (`script/vagrant.sh update`). Re-run `ensure-qemu-binfmt.sh` on each node after pulling the qemu 9 upgrade. Native arm64 Node is not used; expect slower builds than on amd64.

## Dependencies

Dependencies are managed using `npm`. `npm` expects dependencies specified under the [`dependencies` attribute](https://www.npmjs.org/doc/files/package.json.html#dependencies) inside the `package.json` file, which is just a simple object, with package names as the keys, mapping to version ranges.

### Specifying a Node.js Version

Node.js version can be specified using the [`engines` section](https://docs.npmjs.com/cli/v10/configuring-npm/package-json#engines) of the `package.json` file. The heroku-24 Node.js buildpack resolves modern Node and npm releases; pin versions there rather than shipping ancient `0.10` engines.

### Example package.json

```json
{
  "name": "node-example",
  "version": "0.0.1",
  "dependencies": {
    "express": "4.10.0",
    "stylus": "0.49.2"
  },
  "devDependencies": {
    "grunt": "0.4.5"
  },
  "engines": {
    "node": "22.x"
  }
}
```

## Custom Build Step

For apps that require extra processing before the deploy, an npm `postinstall` script can be added. It will run immediately after `npm install --production`, and the release environment will be available. Note that the buildpack doesn't install `devDependencies`. Should you require any of those, they should be moved to `dependencies`.

## Default Process Type

Node.js apps can be deployed without a `Procfile`. If no `Procfile` is present, the buildpack will expect a [`scripts.start`](https://www.npmjs.org/doc/misc/npm-scripts.html) key in the `package.json` file, and the default process type `web` will run the script using `npm start`.

## Run Jobs

Besides the usual utilities, `npm` and `node` are in `PATH` and are available directly via `flynn run`.

```
$ flynn run node -v
v22.x
```
