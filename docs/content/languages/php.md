---
title: PHP
layout: docs
---

# PHP

Flynn supports deploying PHP applications using the PHP runtime, with a choice
of either [Apache2](http://httpd.apache.org/) or
[Nginx](http://nginx.org/) as the web server.

Flynn uses the [PHP buildpack](https://github.com/heroku/heroku-buildpack-php) on
the `heroku-24` stack to detect, compile, and release PHP applications.

## Detection

Flynn detects a PHP application by the presence of a `composer.json` file in the root directory.
It then uses [Composer](https://getcomposer.org/) (A PHP dependency management tool) to
determine, download, and install the dependencies of the application.

If an application has no Composer dependencies, it should still include an empty `composer.json`
file to be detected as a PHP application.

## Dependencies

### Packages

The primary use of the `composer.json` file is to declare package dependencies.

Here is an example `composer.json` file to declare a dependency on the
[monolog](https://github.com/Seldaek/monolog) package:

```json
{
  "require": {
    "monolog/monolog": "1.11.*"
  }
}
```

Running `composer install` will download and install the necessary dependencies and
create a `composer.lock` file. This contains a snapshot of all the packages and versions
which were installed. The `composer.lock` file must be present when the application is
deployed so that Flynn can determine the exact versions of packages to install.

Composer also creates a `vendor/autoload.php` file which can be required, leading to
classes from the dependent packages being autoloaded. So to use the `monolog` package
in your application:

```php
require 'vendor/autoload.php';

// you can now reference Monolog
use Monolog\Logger;

$log = new Logger('my-application');
...
```

*For more information on the `composer.json` format, see the [Composer JSON schema page]
(https://getcomposer.org/doc/04-schema.md).*

### Environment-Specific

There are some packages you may use locally that don't need to be installed in
production. Flynn calls `composer install` with `--no-dev`, which will skip
installing any packages in the `require-dev` property of `composer.json`.

For example, you may use the `phpunit` package locally, but this is seldom
required in production, so you should put this into the `require-dev` property:

```json
{
  "require": {
    "monolog/monolog": "1.11.*"
  },
  "require-dev": {
    "phpunit/phpunit": "4.3.*"
  }
}

```

For reference, here is the full Composer command that will be run:

```
composer install \
  --no-dev \
  --prefer-dist \
  --optimize-autoloader \
  --no-interaction
```

*For more information on the `composer install` command, see the [composer install page](https://getcomposer.org/doc/03-cli.md#install).*

### PHP Runtime

By default, applications run on a current PHP 8.x from the heroku-24 buildpack.
Pin a version with a Composer `php` requirement:

```json
{
  "require": {
    "php": "^8.3"
  }
}
```

HHVM is not available on heroku-24.

*It is advisable to use Composer's version operators so your application stays
on a supported minor release. See the [Composer version
guide](https://getcomposer.org/doc/articles/versions.md).*

## Process Types

The type of processes that your application supports can be declared in a `Procfile` in the
root directory, which contains a line per type in the format `TYPE: COMMAND`.

### web

The `web` process type gets an allocated HTTP route and a corresponding `PORT` environment
variable, so it typically starts an HTTP server for your application.

Flynn supports two web servers out-of-the-box:

#### Apache2

To start Apache2 (and PHP-FPM), use the `heroku-php-apache2` script:

```
web: vendor/bin/heroku-php-apache2
```

#### Nginx

To start Nginx (and PHP-FPM), use the `heroku-php-nginx` script:

```
web: vendor/bin/heroku-php-nginx
```

#### Default

If your application does not have a `Procfile`, a default `web` process will be used
to start Apache with the runtime defined in `composer.json` (i.e. either
`vendor/bin/heroku-php-apache2` or `vendor/bin/heroku-hhvm-apache2`).
