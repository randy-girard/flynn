---
title: Python
layout: docs
---

# Python

Python is supported by the [Python
buildpack](https://github.com/heroku/heroku-buildpack-python) on the default
`heroku-24` stack. That stack runs **Python 3** only.

## Detection

The Python buildpack is used if the repository contains a `requirements.txt` or
`Pipfile` / `poetry.lock` (see the buildpack for the full detection list).
Django applications are detected by the presence of a `manage.py` file. If
`manage.py` is found, `manage.py collectstatic` is run during the compilation
process.

## Dependencies

Dependencies are installed with [`pip`](https://pip.pypa.io/). For a typical app,
list them in `requirements.txt`:

```
Flask>=3.0
gunicorn
```

## Specifying a Runtime

Pin the interpreter with `runtime.txt` or `.python-version`. The buildpack
selects a supported Python 3 on heroku-24 if you do not pin one.

```
python-3.12.8
```

See the [Python buildpack](https://github.com/heroku/heroku-buildpack-python)
for the current list of supported versions. Python 2 is not available.

## Default Process Types

No default process types are defined for this buildpack, so a `Procfile` is
needed. To deploy [Gunicorn](https://gunicorn.org), for example:

```
web: gunicorn hello:app --log-file -
```
