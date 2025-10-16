# DCHero 

**DCHero** is a Go-based tool designed to identify **Dependency Confusion vulnerability** in web applications by analyzing URLs that point to dependency manifests (such as `package.json`, `requirements.txt`, `go.mod`, etc.) as well as JavaScript/TypeScript source files that import public packages.

The tool automatically checks whether listed packages are **unregistered (unclaimed)** in public registries (`npm` and `PyPI`), which may indicate a **potential Dependency Confusion attack surface**.

---

## Features

- Native support for **Node.js (npm)** and **Python (PyPI)**.  
- Scans both **manifest files** and **JS/TS source code** for imports/requires.  
- Fully **concurrent** execution with thread control.  
- **Silent design** — only prints relevant findings.  
- Adjustable performance with `-t` flag (1–100 threads).  
- Fault-tolerant — ignores network and parsing errors silently.  

---

## ⚙️ Usage

### Input
The tool reads **URLs** from standard input (`stdin`).

Example:

```bash
cat urls.txt | ./dchero
```

### Flags

| Flag | Description | Default |
|------|--------------|----------|
| `-t` | Number of concurrent threads (1–100) | 20 |
| `-silent` | Suppress banner output | false |

---

## Examples

### Simple scan

```bash
cat urls.txt | ./dchero
```

### Scan with 50 threads

```bash
cat urls.txt | ./dchero -t 50
```

### Silent scan (no banner)

```bash
cat urls.txt | ./dchero -silent
```

---

## Output

The tool prints only vulnerabilities found, formatted as:

```
[<package>|<http-status>|<language>] <url>
```

Example:

```
[internal-lib|404|js] https://example.com/assets/package.json
[analytics-toolkit|404|python] https://api.example.org/requirements.txt
```

- `404` → package **not found** on the public registry (potentially unclaimed).  
- `js` / `python` → detected language.  
- Red brackets (`[ ... ]`) indicate a positive finding.  

---

## Detected file types

- **JavaScript / Node.js**
  - `package.json`
  - `package-lock.json`
  - `yarn.lock`
  - `pnpm-lock.yaml`
  - `.js`, `.ts`, `.mjs`, `.cjs`

- **Python**
  - `requirements.txt`
  - `pyproject.toml`
  - `Pipfile`, `Pipfile.lock`
  - `constraints.txt`
  - `setup.py`

- **Go / PHP**
  - `go.mod`
  - `composer.json`

---

## Performance

- Default 20 threads → best balance of speed and stability.  
- Use `-t 100` for faster results on stronger environments.  
- Each dependency is validated with an internal HEAD request cache.  

---

## Installation

### Requires Go ≥ 1.21

```bash
git clone https://github.com/luq0x/dchero
cd dchero
go build -o dchero
```

Or install directly with:

```bash
go install github.com/luq0x/dchero@latest
```

---

## 💡 Pipeline usage example

```bash
cat urls.txt | ./dchero -t 80 -silent | tee results.txt
```

You can combine **DCHero** with tools like **gau**, **waybackurls**, **katana**, **gospider**, or **hakrawler** to automatically gather and analyze URLs.


