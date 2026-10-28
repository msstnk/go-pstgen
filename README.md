# go-pstgen

**Motivation**

So your company just acquired another company, and you're scheduled to receive a mountain of PST files right before Day 1. You have to figure out how long the import will take, and you need to know before the cutover weekend. We've all been there, right?

**What it does**

`go-pstgen` generates dummy PST files from the [MS-PST] spec, and that's about all it does. The text is gibberish, but the sizes, folders and structure look like the real thing. Feed it a config, hit run, and a pile of fake mail pours out at over 1 GiB/s.

The repository also ships a second tool, [`go-pstprune`](#go-pstprune), which cuts folders such as `Recoverable-Items` out of existing PST files (for example Purview exports) before a migration.

**Who it's for**

- You need TB-class migration data to test an import, and you need it soon.
- You don't want to build the PST files by hand.
- You don't want real mailbox data anywhere near a test run.
- You want to know how many hours per TB the import takes before the cutover meeting, not during it.

**Features**

- Pure Go, no CGO. One binary for Linux and one for Windows.
- Purview-style folders: `Inbox`, `Sent Items`, `Deleted Items`, `Recoverable-Items` (hyphenated, as Purview names it) with `Deletions`, `Purges`, `Versions` and `DiscoveryHolds`.
- Mailbox sizes follow a normal (or log-normal) distribution and message sizes a log-normal one, with optional attachments.
- Files are split into `<UPN>.pst`, `<UPN>-2.pst`, ... when they get too big.
- Parallel workers, so 100 mailboxes finish in seconds.
- Set a `seed` and you get the same output every time.
- `go-pstprune`: detach folders (e.g. `Recoverable-Items/*`) from real PST files in place and zero-fill their data.

[MS-PST]: https://learn.microsoft.com/en-us/openspecs/office_file_formats/ms-pst/

---

## Install

All you need is Go. No CGO. Install straight from GitHub, no clone required:

```sh
go install github.com/msstnk/go-pstgen/cmd/go-pstgen@latest
go install github.com/msstnk/go-pstgen/cmd/go-pstprune@latest   # optional, see go-pstprune below
```

The binaries land in `$(go env GOPATH)/bin` (or `$GOBIN` if set).

`go install` ships only the binary, so there is no `config.json` to go with it. Create your own by hand (see [config.json](#configjson)) and point `--config` at it. Leave out any key you don't need; the defaults apply.

## Build

Or build from a clone, one line per platform:

```sh
# Linux
CGO_ENABLED=0 go build -o bin/go-pstgen ./cmd/go-pstgen
CGO_ENABLED=0 go build -o bin/go-pstprune ./cmd/go-pstprune
# Windows
GOOS=windows CGO_ENABLED=0 go build -o bin/go-pstgen.exe ./cmd/go-pstgen
GOOS=windows CGO_ENABLED=0 go build -o bin/go-pstprune.exe ./cmd/go-pstprune
```

## Place config.json

Create a `config.json` file in current directory, or anywhere you like, and point `--config` at it when running `go-pstgen`.

Most runs only need a few keys. For 24 mailboxes of about 100 MB on 8 cores (write all 24 UPNs out in your real file; the `"..."` here is just shorthand):

```json
{
  "mailboxes": [
    "user01@example.com",
    ...
    "user24@example.com"
  ],
  "mailbox_mean_mb": 100,
  "mailbox_stddev_mb": 20,
  "workers": 8
}
```

Details on all available keys are in the [config.json](#configjson) section below.

## Run

It defaults to `--config config.json`, so you can omit the flag.
```sh
bin/go-pstgen --config config.json
```

## go-pstprune

`go-pstprune` cuts folders out of existing PST files. Purview exports almost always contain the hold area under `Recoverable-Items` (`Deletions`, `Purges`, `Versions`, `DiscoveryHolds`, ...), and this is a pre-processing step to keep it from showing up after migration. It also works on the files `go-pstgen` generates, which makes it easy to try out.

```sh
go-pstprune --file 'output/*.pst' --path 'Recoverable-Items/*'
go-pstprune --dry-run --file 'output/*.pst' --path 'Recoverable-Items/*'   # report only
```

| Option | Description |
|---|---|
| `--file` | PST file or glob. Repeatable; extra arguments count as files too |
| `--path` | Folder path to prune. Repeatable; each segment is a case-insensitive glob |
| `--no-fill` | Detach only; do not zero-fill the blocks |
| `--dry-run` | Report what would be pruned without modifying anything |

For every matching folder it:

1. detaches the folder, its subfolders, messages and tables from the NBT (with a trailing `*`: the messages directly in the parent, and the matching rows of its contents tables, content counts and all),
2. removes the folder's row from the parent's hierarchy table, so no dangling folder is left behind, and clears the parent's `hasSubfolders` flag (in its properties and in its own table row) when no subfolder is left,
3. fills the blocks that only those nodes used with `0x00` and drops them from the BBT (skip with `--no-fill`).

Files are modified **in place**, so work on copies. Notes:

- `--file` and `--path` can be repeated. `--file` takes globs, and extra arguments are treated as files, so an unquoted `output/*.pst` works too.
- Each `--path` segment is a case-insensitive glob. Paths are tried from the root folder and from the top of the mailbox (`Top of Personal Folders`). A trailing `*` matches items as well as folders, like a file glob: `Recoverable-Items/*` prunes the subfolders and the messages directly in the folder, and keeps the (now empty) folder itself; `Recoverable-Items` prunes the folder too.
- Blocks still referenced by a node you keep are never touched. Files with no match are skipped (exit code 0); any other error makes the exit code 1.
- `--dry-run` reports what would be pruned without writing. The root and the top-level folder cannot be pruned.
- The AMap and the file size are left as they are, so the file does not shrink. Unicode PST (wVer 23) only.

## config.json

Leave a key out and you get the default from the table below. A key that doesn't exist is an error, so typos don't slip by.

| Key | Default | Description |
|---|---|---|
| `output_dir` | `"./output"` | Output directory. Files are written flat as `<output_dir>/<UPN>.pst` (`<UPN>-2.pst`, `<UPN>-3.pst` ... when split) |
| `mailboxes` | `["user01@example.com"]` | List of UPNs of the mailboxes to generate (empty is an error) |
| `mailbox_mean_mb` | `1000` | Mean mailbox size in MB. A sampled size is clamped to between the larger of 10% of the mean and 1 MB, and 10 times the mean |
| `mailbox_stddev_mb` | `200` | Standard deviation of the mailbox size (MB). Ignored when `mailbox_log_variance` is set |
| `mailbox_log_variance` | `null` | If set (greater than 0), mailbox sizes follow a log-normal distribution with this log variance instead of the normal one: a long tail toward large mailboxes while the mean stays `mailbox_mean_mb` (`mailbox_stddev_mb` is ignored). Around `1.0`-`1.3` looks realistic, but the spread of file sizes gets wide, so handle with care. If `null`, the normal distribution is used |
| `workers` | number of CPUs | Parallelism (values below 1 become 1; capped at the number of mailboxes) |
| `total_messages` | `0` | Number of messages per mailbox. If 0 or less, messages are generated until the size target is reached |
| `min_size_kb` | `10` | Minimum size of a message (KB, at least 1) |
| `max_size_kb` | `10240` | Maximum size of a message (KB, at least `min_size_kb`) |
| `mean_size_kb` | `150` | Mean message size (log-normal distribution) in KB |
| `log_variance` | `0.5` | Log variance of the message size |
| `attachment_ratio` | `0.3` | Fraction of messages with an attachment |
| `seed` | `null` | Random seed. If `null`, the current time is used |
| `crypt_method` | `"permute"` | PST encryption method: `none` / `permute` / `cyclic` |
| `holds_ratio` | `1.0` | Fraction of mailboxes (0.0-1.0) that have the hold folders `Purges`, `Versions` and `DiscoveryHolds` (holds are in effect). The other mailboxes get no such folders, and their share of messages is spread over the other folders |
| `end_date` | `""` (run time) | Latest message date. RFC 3339 (`2026-03-31T12:00:00Z`) or `YYYY-MM-DD` (end of that day, UTC). If empty, the run time is used |
| `range_days` | `365` | Messages are dated uniformly within this many days before `end_date` (at least 1) |
| `split_size_mb` | `10240` | Maximum size of one PST file (MB). Output moves to the next file once the limit is expected to be exceeded. If 0 or less, files are not split |

Here's an example with every key set (everything except `mailboxes`, `workers` and `seed` is the default):

```json
{
  "output_dir": "./output",
  "mailboxes": ["user01@example.com", "user02@example.com"],
  "mailbox_mean_mb": 1000,
  "mailbox_stddev_mb": 200,
  "mailbox_log_variance": null,
  "workers": 4,
  "total_messages": 0,
  "min_size_kb": 10,
  "max_size_kb": 10240,
  "mean_size_kb": 150,
  "log_variance": 0.5,
  "attachment_ratio": 0.3,
  "seed": 12345,
  "crypt_method": "permute",
  "holds_ratio": 1.0,
  "end_date": "",
  "range_days": 365,
  "split_size_mb": 10240
}
```

## Generated messages

### Example

The text is gibberish, but word lengths and letter frequencies match real English—so at a glance, it looks legitimate.

```
Message-ID: <3f9a1c07-5be2-04d1-9e6a-7c21d8f04b3e@contoso.com>
Date: Tue, 11 Mar 2026 04:27:51 +0000
From: Tenos Hwarle <tenos.hwarle42@contoso.com>
To: Ota Mirsen <ota.mirsen7@example.org>, Eadrin Sot <eadrin.sot88@example.com>
Cc: Nialer Coth <nialer.coth15@corp.test>
Subject: Wen ariset tho delanhe sor.
MIME-Version: 1.0
X-Mailer: Microsoft Outlook 16.0
X-Priority: 3 (Normal)
Content-Type: text/plain; charset=UTF-8

Hes ronetal ost aw ferind thae lo minret cas. Dorte en the sialy rot hamse ec veron. Ilt aste mo tirean pe hos donwerl.
Fane so rhet idatin wos ce lerath nod ah. Tre orsen ...

[Attachment] k3f0q9z1m7wa.attach (application/octet-stream)
```

### Generation rules

**Folder layout and distribution**

```
Top of Personal Folders
├── Inbox              40%
├── Sent Items         25%
├── Deleted Items      15%
└── Recoverable-Items   (no messages of its own)
    ├── Deletions       8%
    ├── Purges          5%
    ├── Versions        4%
    └── DiscoveryHolds  3%
```

`Deletions` is user-visible (Recover Deleted Items). `Purges`, `Versions` and `DiscoveryHolds` are hidden from users and exist only on mailboxes under a hold; each mailbox gets them with probability `holds_ratio`. The ones that don't get them simply skip those folders, and the other weights scale up to fill the gap.

Each message lands in a random folder according to the weights above, and inside a folder the messages are sorted oldest first.

**Splitting**

- When the next message would push the estimated file size past `split_size_mb`, the current file is closed and writing continues in `<UPN>-2.pst`, `<UPN>-3.pst` ... Every file holds at least one message.
- Every file has the same folder layout. Messages fill the first file first, in the folder and date order above.

**Counts and sizes**

- The target size of each mailbox is sampled from the normal distribution given by `mailbox_mean_mb` / `mailbox_stddev_mb`, or, if `mailbox_log_variance` is set, from a log-normal distribution with mean `mailbox_mean_mb`.
- Message sizes are sampled from a log-normal distribution with mean `mean_size_kb` and log variance `log_variance`, and clamped to `[min_size_kb, max_size_kb]`.
- If `total_messages` > 0, exactly that many messages are generated; otherwise messages are generated until the total size reaches the target size.
- Each message has an attachment with probability `attachment_ratio`.

**Header fields**

| Field | Rule |
|---|---|
| Date | Uniformly random within `range_days` days before `end_date` (UTC; defaults: 365 days before the run time). Sent time = received time |
| People | Display name is `Word Word` (each word capitalized); address is `word.word<1-99>@<domain>` |
| Domain | Chosen from `example.com`, `example.net`, `example.org` (RFC 2606), `corp.test`, `mail.test`, `mail.invalid`, `corp.example` (reserved TLDs `.test` / `.invalid` / `.example`, RFC 2606 / 6761) and `contoso.com` (Microsoft's sample domain). |
| From | 1 person |
| To / Cc | 1-5 people each |
| Subject | A sentence of 3-8 words (capitalized, ending with a period) |
| Message-ID | `<8-4-4-4-12 hex digits@sender's domain>` |
| Other | `MIME-Version`, `X-Mailer: Microsoft Outlook 16.0`, `X-Priority: 3 (Normal)` and `Content-Type: text/plain; charset=UTF-8` are always added. Message class is `IPM.Note` |

**Body**

- At startup, a pool of 200,000 words is built, and from it a sentence pool of 5-14-word sentences. A line break is inserted every 3-5 sentences.
- The body is cut from the sentence pool starting at a sentence start and extended to the end of a sentence (period).
- Without attachment: the body takes (message size − approximate header size of 600 bytes − subject length); the character count is half of that because it is stored as UTF-16. Minimum 200 bytes.
- With attachment: the body is kept to about 200 bytes and the rest is assigned to the attachment.

**Attachments**

- At most one per message. The file name is 12 characters of `[a-z0-9]` + `.attach`, and the MIME type is `application/octet-stream`.
- The content is random ASCII made of alphanumerics, spaces and newlines.


## Tested environments

| What | Status |
|---|---|
| Generating PST files on Linux | Verified |
| Generating PST files on Windows | Verified |
| Importing the generated files in the Outlook for Microsoft 365 client | Verified |
| Microsoft Purview (PST import) | Not verified yet |
| `go-pstprune` output passes the repository's strict PST verifier (generated files and an Outlook-created PST) | Verified |
| `go-pstprune` output opened in the Outlook client or imported by a migration tool | Not verified yet |

Tried it with another migration tool? PRs and issue reports are welcome.

## Benchmark

Here's what generating 100 mailboxes took, with a mean of 100 MB, a standard deviation of 20 MB and 8 workers.

```json
{
  "mailboxes": ["user001@example.com", "...", "user100@example.com"],
  "mailbox_mean_mb": 100,
  "mailbox_stddev_mb": 20,
  "workers": 8,
  "seed": 12345
}
```

| Item | Result |
|---|---|
| Run time (3 runs) | 7.41 s / 7.53 s / 5.87 s |
| Total output size | 10.2 GiB (100 files) |
| Total messages | 68,089 |
| PST size (min / median / max) | 36.5 / 104.2 / 156.6 MB |
| Throughput | about 1.4 GiB/s |
| Peak memory usage (RSS) | about 190 MB |

## FAQ

**Q: The seller refuses to disclose their mailbox sizes. How do I set `mailbox_mean_mb`?**  
A: When the seller treats basic storage statistics like classified intelligence, look up their license tier. Exchange Online Plan 1 caps mailboxes at 50 GB; Plan 2, E3, and E5 cap them at 100 GB. Users treat storage limits as targets. Plug in a pessimistic baseline like `20000` (20 GB) or `40000` (40 GB), set `"mailbox_log_variance": 1.2` for the hoarders who never empty Deleted Items, and hand the resulting projected timeline to your PMO.

**Q: The PMO insists the cutover window is strictly 12 hours. Can `go-pstgen` help?**  
A: `go-pstgen` can produce files at over 1 GiB/s, but it cannot alter target-tenant throttling, your outbound internet pipe, or the laws of physics. Its primary function is providing the math that proves 12 hours is impossible on a Tuesday afternoon, rather than discovering it at 3:00 AM on Sunday during the cutover call.

**Q: Our migration vendor claims their tool handles 500 GB per hour. Should I trust that?**  
A: Vendors measure throughput in isolated lab environments with sequential, pristine files and zero API throttling. Populate `config.json` to generate 30 GiB PSTs for 100 mailboxes packed with 150 KiB messages, run an actual benchmark, and invite their account executive to watch the live throughput graph with you.

**Q: Why not request sanitized, real PST samples from the seller?**  
A: You can ask, but unless your M&A team somehow negotiated a clause in the SPA forcing their engineers to scrub production mailboxes for a pre-Day 1 test, they will never do this. Your best hope is that the seller's engineers also found `go-pstgen` and quietly run it on their end to get you off their back.

**Q: Can I use this to test edge cases in our migration tool?**  
A: No. `go-pstgen` is built strictly for throughput benchmarking and API concurrency testing. It does not simulate corrupt PST structures, duplicate Message-IDs, malformed MIME parts, or nested calendar exceptions. Those delightful edge cases will remain safely hidden until 3:00 AM on cutover night, exactly as you anticipated.

**Q: The email bodies are gibberish. Will our import tool reject them?**  
A: No. Import tools parse byte counts, headers, and folder nodes, not literary prose. The binary structure adheres strictly to the MS-PST specification: valid UTF-16 encoding, legitimate MIME headers, and compliant node trees. Your migration tool will ingest them happily, and your DLP scanners will find absolutely nothing to report—unless the infinite monkey theorem kicks in and accidentally types out next quarter's board slides.

## License

[MIT](LICENSE)

### Microsoft specification and patents

This is an independent implementation, written from the publicly available [MS-PST]: Outlook Personal Folders (.pst) File Format Specification. It is not affiliated with or endorsed by Microsoft. Microsoft, Outlook and Purview are trademarks of Microsoft Corporation.

- The MIT License covers only the code in this repository. It grants no rights under any Microsoft patent, copyright or trademark; Microsoft reserves them.
- [MS-PST] is covered by the [Microsoft Open Specification Promise](https://learn.microsoft.com/en-us/openspecs/dev_center/ms-devcentlp/1c24c7c8-28b0-4ce1-a47d-95fe1ff504bc), a personal promise to each implementer, not a license. It does not pass to downstream users through the MIT License; check its terms before redistributing or using this software commercially.
