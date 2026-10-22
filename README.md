# go-pstgen

**Motivation**

So your company just acquired another company, and a mountain of PST files are about to be handed to you. You have to figure out how long the importing process will take, and you want to do it before the cutover weekend. We've all been there, right?

**What it does**

`go-pstgen` generates dummy PST files from the [MS-PST] spec, and that's about all it does. The text is gibberish, but the sizes, folders and structure look like the real thing. Write a config, run it, and a pile of fake mail comes out at around 1.4 GiB/s.

**Who it's for**

- You need TB-class migration data to test an import, and you need it soon.
- You don't want to build the PST files by hand.
- You don't want real mailbox data anywhere near a test.
- You want to know how many hours per TB the import takes before the cutover meeting, not during it.

**Features**

- Pure Go, no CGO. One binary for Linux and one for Windows.
- Purview-style folders: `Inbox`, `Sent Items`, `Deleted Items`, `Recoverable Items` and `Legal Holds`.
- Mailbox sizes follow a normal distribution and message sizes a log-normal one, with optional attachments.
- Files are split into `<UPN>.pst`, `<UPN>-2.pst`, ... when they get too big.
- Parallel workers, so 100 mailboxes finish in seconds.
- Set a `seed` and you get the same output every time.

[MS-PST]: https://learn.microsoft.com/en-us/openspecs/office_file_formats/ms-pst/

---

## Build

All you need is Go. No CGO, so the build is one line per platform.

Want to skip cloning? Install it straight from GitHub:

```sh
go install github.com/msstnk/go-pstgen/cmd/go-pstgen@latest
```

The binary lands in `$(go env GOPATH)/bin` (or `$GOBIN` if set). Or build from a clone:

```sh
# Linux
CGO_ENABLED=0 go build -o bin/go-pstgen ./cmd/go-pstgen
# Windows
GOOS=windows CGO_ENABLED=0 go build -o bin/go-pstgen.exe ./cmd/go-pstgen
```

## Run

`--config config.json` is the default, so you can leave it out.
```sh
bin/go-pstgen --config config.json
```

## config.json

Leave a key out and you get the default from the table below. A key that doesn't exist is an error, so typos don't slip by.

| Key | Default | Description |
|---|---|---|
| `output_dir` | `"./output"` | Output directory. Files are written flat as `<output_dir>/<UPN>.pst` (`<UPN>-2.pst`, `<UPN>-3.pst` ... when split) |
| `mailboxes` | `["user01@example.com"]` | List of UPNs of the mailboxes to generate (empty is an error) |
| `mailbox_mean_mb` | `1000` | Mean mailbox size (normal distribution) in MB. A sampled size is never below the larger of 10% of the mean and 1 MB |
| `mailbox_stddev_mb` | `200` | Standard deviation of the mailbox size (MB) |
| `workers` | number of CPUs | Parallelism (values below 1 become 1; capped at the number of mailboxes) |
| `total_messages` | `0` | Number of messages per mailbox. If 0 or less, messages are generated until the size target is reached |
| `min_size_kb` | `10` | Minimum size of a message (KB, at least 1) |
| `max_size_kb` | `10240` | Maximum size of a message (KB, at least `min_size_kb`) |
| `mean_size_kb` | `150` | Mean message size (log-normal distribution) in KB |
| `log_variance` | `0.5` | Log variance of the message size |
| `attachment_ratio` | `0.3` | Fraction of messages with an attachment |
| `seed` | `null` | Random seed. If `null`, the current time is used |
| `crypt_method` | `"permute"` | PST encryption method: `none` / `permute` / `cyclic` |
| `legal_holds_ratio` | `1.0` | Fraction of mailboxes (0-1) that have a `Legal Holds` folder. Mailboxes without it get no such folder, and its share of messages is spread over the other folders |
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
  "workers": 4,
  "total_messages": 0,
  "min_size_kb": 10,
  "max_size_kb": 10240,
  "mean_size_kb": 150,
  "log_variance": 0.5,
  "attachment_ratio": 0.3,
  "seed": 12345,
  "crypt_method": "permute",
  "legal_holds_ratio": 1.0,
  "end_date": "",
  "range_days": 365,
  "split_size_mb": 10240
}
```

## Generated messages

### Example

The words are random and mean nothing. Only the word lengths and letter frequencies are matched to English, so the text looks English-like.

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
└── Recoverable Items  10%
    └── Legal Holds    10%
```

Each mailbox gets a `Legal Holds` folder with probability `legal_holds_ratio`. The ones that don't get it simply skip the folder, and the other weights scale up to fill the gap.

Each message lands in a random folder according to the weights above, and inside a folder the messages are sorted oldest first.

**Splitting**

- When the next message would push the estimated file size past `split_size_mb`, the current file is closed and writing continues in `<UPN>-2.pst`, `<UPN>-3.pst` ... Every file holds at least one message.
- Every file has the same folder layout. Messages fill the first file first, in the folder and date order above.

**Counts and sizes**

- The target size of each mailbox is sampled from the normal distribution given by `mailbox_mean_mb` / `mailbox_stddev_mb`.
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

If you try it with a migration tool, results are welcome.

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

## Directory layout

- `cmd/go-pstgen/` — command-line entry point
- `internal/config/` — `config.json` loading and validation
- `internal/gen/` — message planning, text generation and the worker pool
- `pst/` — PST (NDB/LTP) writer library

## License

[MIT](LICENSE)

### Microsoft specification and patents

This is an independent implementation, written from the publicly available [MS-PST]: Outlook Personal Folders (.pst) File Format Specification. It is not affiliated with or endorsed by Microsoft. Microsoft, Outlook and Purview are trademarks of Microsoft Corporation.

- The MIT License covers only the code in this repository. It grants no rights under any Microsoft patent, copyright or trademark; Microsoft reserves them.
- [MS-PST] is covered by the [Microsoft Open Specification Promise](https://learn.microsoft.com/en-us/openspecs/dev_center/ms-devcentlp/1c24c7c8-28b0-4ce1-a47d-95fe1ff504bc), a personal promise to each implementer, not a license. It does not pass to downstream users through the MIT License; check its terms before redistributing or using this software commercially.
