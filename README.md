# webscour

A concurrent Go web scraper. Give it a starting URL and a set of file
extensions; it crawls the site, follows links across the domain (and its
subdomains), and downloads every matching file into a folder tree that mirrors
where the file lives on the site.

## Features

- **Concurrent crawl** — one goroutine per discovered URL, with a bounded worker
  pool capping simultaneous HTTP requests (`-workers`).
- **Scoped to the registrable domain** — follows `example.com` and any subdomain
  (`www.`, `blog.`, …); external sites are ignored.
- **Each page scanned once** — URLs are deduplicated (fragments stripped).
- **Respects robots.txt** — honors `Allow`/`Disallow` and per-host `Crawl-delay`.
- **Site-mirroring layout** — files are written to
  `‹out›/‹root-domain›/‹ext›/‹url path dirs›/‹filename›`, so each domain gets a
  per-file-type sitemap on disk.
- **Resumable** — files that already exist on disk are skipped; downloads are
  written atomically (temp file + rename) so an interrupted run never leaves a
  partial file that looks complete.

## Install / build

```sh
go build -o webscour ./...
```

## Usage

```sh
./webscour -url https://www.example.com -ext pdf,docx,zip -out downloads -workers 16
```

### Flags

| Flag       | Default                | Description                                            |
| ---------- | ---------------------- | ------------------------------------------------------ |
| `-url`     | _(required)_           | Starting URL — absolute `http`/`https`.                |
| `-ext`     | `pdf`                  | Comma-separated extensions to download (no dots).      |
| `-workers` | `16`                   | Max concurrent in-flight HTTP requests.                |
| `-out`     | `downloads`            | Output directory.                                      |
| `-ua`      | `webscour/1.0 …`       | User-Agent header and robots.txt agent token.          |
| `-timeout` | `30s`                  | Per-request HTTP timeout.                              |

## Output layout

A link to `https://www.example.com/files/board/2026-01.pdf` is saved as:

```
downloads/
└── example.com/          ← registrable domain (subdomains collapse here)
    └── pdf/              ← file extension
        └── files/
            └── board/
                └── 2026-01.pdf
```

## Notes / limitations

- Scope is the **registrable domain** (eTLD+1); all subdomains of it are
  crawled and their files collapse under one domain folder.
- Crawl-delay is enforced **per host**; different hosts proceed in parallel.
- Deduplication is URL-based, so distinct URLs serving identical content
  (e.g. `/` vs `/index.html`) may each be fetched once.
- Only `<a href>`/`src` attributes are followed; JavaScript-rendered links are
  not discovered.
