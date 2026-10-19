use std::collections::HashSet;
use std::fs::File;
use std::io::{BufRead, BufReader, BufWriter, Write};
use std::time::Instant;

use fnv::FnvBuildHasher;
use gaoya::minhash::{calculate_minhash_params, MinHashIndex, MinHasher, MinHasher32};
use gaoya::text::{shingle_tokens, whitespace_split};

const VERSION: &str = env!("CARGO_PKG_VERSION");

const HELP: &str = "\
revserp-gaoya: near-duplicate text matcher (gaoya MinHash LSH)

usage: revserp-gaoya --input PATH --out-dir DIR [options]

required:
  --input PATH        JSONL input, one object per line: {\"id\": string|number, \"text\": string}
  --out-dir DIR       output directory (created if missing); writes pairs.jsonl + metrics.json

options:
  --threshold F       minhash similarity threshold in (0,1] (default: 0.9)
  --num-hashes N      signature length (default: 128)
  --bands B           explicit LSH band count (default: derived from threshold)
  --band-width R      explicit LSH rows per band (default: derived from threshold)
                      explicit bands*band-width must not overflow and must be <= --num-hashes
  --shingle W         shingle width in words, >=1 (default: 5)
  --lowercase BOOL    true|false (default: true)
  --seed S            hasher seed (default: 3)
  --help, -h          print this help
  --version, -V       print version

input rules:
  id must be a JSON string or number; numbers are normalized to their
    decimal string (e.g. 1 -> \"1\"). ids are always emitted as JSON strings.
  text must be a JSON string (empty string is valid and is skipped).
    a missing/non-string text is an error.
  duplicate ids are an error (exit 1) to prevent silent data loss.
output pairs.jsonl schema (one object per line):
  {\"left_id\": <string>, \"right_id\": <string>, \"score\": <f64>, \"score_kind\": \"minhash\"}
  score is the minhash signature jaccard estimate, threshold applied to that
  estimate, NOT exact shingle jaccard.
exit codes: 0 ok, 1 input/IO error, 2 CLI flag error.
";

struct Record {
    id: String,
    text: String,
}

struct Config {
    input: String,
    out_dir: String,
    threshold: f64,
    num_hashes: usize,
    bands: Option<usize>,
    band_width: Option<usize>,
    shingle: usize,
    lowercase: bool,
    seed: u64,
}

fn usage_error(msg: &str) -> ! {
    eprintln!("revserp-gaoya: error: {msg}");
    eprintln!("{HELP}");
    std::process::exit(2);
}

fn io_error(context: &str, e: impl std::fmt::Display) -> ! {
    eprintln!("revserp-gaoya: error: {context}: {e}");
    std::process::exit(1);
}

fn parse_args() -> Config {
    let mut cfg = Config {
        input: String::new(),
        out_dir: String::new(),
        threshold: 0.9,
        num_hashes: 128,
        bands: None,
        band_width: None,
        shingle: 5,
        lowercase: true,
        seed: 3,
    };
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.is_empty() {
        usage_error("need --input and --out-dir");
    }
    let mut i = 0;
    while i < args.len() {
        let a = args[i].as_str();
        let val = |i: &mut usize| {
            *i += 1;
            if *i >= args.len() {
                usage_error("missing value");
            }
            args[*i].clone()
        };
        match a {
            "--help" | "-h" => {
                print!("{HELP}");
                std::process::exit(0);
            }
            "--version" | "-V" => {
                println!("revserp-gaoya {VERSION}");
                std::process::exit(0);
            }
            "--input" => cfg.input = val(&mut i),
            "--out-dir" => cfg.out_dir = val(&mut i),
            "--threshold" => {
                cfg.threshold = val(&mut i)
                    .parse()
                    .unwrap_or_else(|_| usage_error("bad --threshold"))
            }
            "--num-hashes" => {
                cfg.num_hashes = val(&mut i)
                    .parse()
                    .unwrap_or_else(|_| usage_error("bad --num-hashes"))
            }
            "--bands" => {
                cfg.bands = Some(
                    val(&mut i)
                        .parse()
                        .unwrap_or_else(|_| usage_error("bad --bands")),
                )
            }
            "--band-width" => {
                cfg.band_width = Some(
                    val(&mut i)
                        .parse()
                        .unwrap_or_else(|_| usage_error("bad --band-width")),
                )
            }
            "--shingle" => {
                cfg.shingle = val(&mut i)
                    .parse()
                    .unwrap_or_else(|_| usage_error("bad --shingle"))
            }
            "--lowercase" => {
                cfg.lowercase = match val(&mut i).as_str() {
                    "true" => true,
                    "false" => false,
                    _ => usage_error("bad --lowercase (want true|false)"),
                }
            }
            "--seed" => {
                cfg.seed = val(&mut i)
                    .parse()
                    .unwrap_or_else(|_| usage_error("bad --seed"))
            }
            _ => usage_error(&format!("unknown arg {a}")),
        }
        i += 1;
    }
    if cfg.input.is_empty() || cfg.out_dir.is_empty() {
        usage_error("need --input and --out-dir");
    }
    if !(0.0 < cfg.threshold && cfg.threshold <= 1.0) {
        usage_error("threshold must be in (0,1]");
    }
    if cfg.num_hashes == 0 {
        usage_error("--num-hashes must be >= 1");
    }
    if cfg.shingle == 0 {
        usage_error("--shingle must be >= 1");
    }
    if let Some(b) = cfg.bands {
        if b == 0 {
            usage_error("--bands must be >= 1");
        }
    }
    if let Some(r) = cfg.band_width {
        if r == 0 {
            usage_error("--band-width must be >= 1");
        }
    }
    if cfg.bands.is_some() != cfg.band_width.is_some() {
        usage_error("need both --bands and --band-width, or neither");
    }
    if let (Some(b), Some(r)) = (cfg.bands, cfg.band_width) {
        let product = b
            .checked_mul(r)
            .unwrap_or_else(|| usage_error("--bands * --band-width overflows"));
        if product == 0 {
            usage_error("--bands * --band-width must be >= 1");
        }
        if product > cfg.num_hashes {
            usage_error("--bands * --band-width must be <= --num-hashes");
        }
    }
    cfg
}

fn cmp_ids(a: &str, b: &str) -> std::cmp::Ordering {
    match (a.parse::<i128>(), b.parse::<i128>()) {
        (Ok(x), Ok(y)) => x.cmp(&y).then(a.cmp(b)),
        _ => a.cmp(b),
    }
}

fn tokenize(text: &str, lowercase: bool) -> Vec<String> {
    let lowered;
    let src = if lowercase {
        lowered = text.to_lowercase();
        lowered.as_str()
    } else {
        text
    };
    whitespace_split(src).map(|s| s.to_string()).collect()
}

fn peak_rss_kb() -> Option<u64> {
    let status = std::fs::read_to_string("/proc/self/status").ok()?;
    for line in status.lines() {
        if let Some(rest) = line.strip_prefix("VmHWM:") {
            return rest.split_whitespace().next()?.parse().ok();
        }
    }
    None
}

fn id_json(id: &str) -> String {
    // ids are always emitted as JSON strings: a string id like "001" or "+1"
    // must never be formatted as a raw number (that would be invalid JSON).
    serde_json::to_string(id).unwrap_or_else(|e| io_error("encoding string id", e))
}

fn main() {
    let cfg = parse_args();
    let t_load = Instant::now();
    let file = File::open(&cfg.input)
        .unwrap_or_else(|e| io_error(&format!("cannot open {}", cfg.input), e));
    let mut records: Vec<Record> = Vec::new();
    let mut seen: HashSet<String> = HashSet::new();
    let mut lineno = 0u64;
    for line in BufReader::new(file).lines() {
        lineno += 1;
        let line = line.unwrap_or_else(|e| io_error(&format!("read input {}", cfg.input), e));
        if line.trim().is_empty() {
            continue;
        }
        let v: serde_json::Value = serde_json::from_str(&line)
            .unwrap_or_else(|e| io_error(&format!("bad json line in {}", cfg.input), e));
        let id = match &v["id"] {
            serde_json::Value::String(s) => s.clone(),
            serde_json::Value::Number(n) => n.to_string(),
            _ => io_error(
                &format!(
                    "line {lineno} in {}: id must be a string or number",
                    cfg.input
                ),
                "bad id",
            ),
        };
        if !seen.insert(id.clone()) {
            io_error(
                &format!("line {lineno} in {}: duplicate id {id:?}", cfg.input),
                "duplicate id",
            );
        }
        let Some(text) = v["text"].as_str() else {
            io_error(
                &format!("line {lineno} in {}: text must be a string", cfg.input),
                "bad text",
            );
        };
        records.push(Record {
            id,
            text: text.to_string(),
        });
    }
    let ms_load = t_load.elapsed().as_millis() as u64;

    if records.len() > u32::MAX as usize {
        io_error(
            &format!("input {}: too many records", cfg.input),
            "record count exceeds u32",
        );
    }

    records.sort_by(|a, b| cmp_ids(&a.id, &b.id));

    let (bands, rows) = match (cfg.bands, cfg.band_width) {
        (Some(b), Some(r)) => (b, r),
        _ => calculate_minhash_params(cfg.threshold, cfg.num_hashes),
    };
    let num_hashes = bands * rows;

    let t_match = Instant::now();
    let mut feats: Vec<Vec<String>> = Vec::with_capacity(records.len());
    let mut lsh_pos: Vec<u32> = Vec::new();
    let mut skipped = 0u64;
    for (pos, rec) in records.iter().enumerate() {
        let tokens = tokenize(&rec.text, cfg.lowercase);
        if tokens.is_empty() {
            skipped += 1;
            continue;
        }
        let shingles = if cfg.shingle <= 1 {
            tokens
        } else {
            let refs: Vec<&str> = tokens.iter().map(|s| s.as_str()).collect();
            let s: Vec<String> = shingle_tokens(&refs, cfg.shingle).collect();
            if s.is_empty() {
                skipped += 1;
                continue;
            }
            s
        };
        lsh_pos.push(pos as u32);
        feats.push(shingles);
    }
    for r in &mut records {
        r.text.clear();
        r.text.shrink_to_fit();
    }

    let hasher =
        MinHasher32::new_with_hasher_and_seed(num_hashes, FnvBuildHasher::default(), cfg.seed);
    let t_sign = Instant::now();
    let signatures = hasher.bulk_create_signature(&feats);
    let ms_sign = t_sign.elapsed().as_millis() as u64;
    let n_indexed = feats.len();
    drop(feats);

    let t_index = Instant::now();
    let mut index = MinHashIndex::<u32, u32>::new(bands, rows, cfg.threshold);
    index.par_bulk_insert_pairs(
        (0..n_indexed as u32)
            .zip(signatures.iter().cloned())
            .collect(),
    );
    let ms_index = t_index.elapsed().as_millis() as u64;

    let t_query = Instant::now();
    let sims = index.par_bulk_query_return_similarity(&signatures);
    let ms_query = t_query.elapsed().as_millis() as u64;

    let mut pairs: Vec<(u32, u32, f64)> = Vec::new();
    for (i, cands) in sims.iter().enumerate() {
        for (j, s) in cands {
            if i as u32 != *j {
                let (a, b) = if (i as u32) < *j {
                    (i as u32, *j)
                } else {
                    (*j, i as u32)
                };
                pairs.push((a, b, *s));
            }
        }
    }
    pairs.sort_by(|x, y| x.0.cmp(&y.0).then(x.1.cmp(&y.1)));
    pairs.dedup_by(|a, b| a.0 == b.0 && a.1 == b.1);
    let ms_match = t_match.elapsed().as_millis() as u64;
    let rss = peak_rss_kb();

    if let Err(e) = std::fs::create_dir_all(&cfg.out_dir) {
        io_error(&format!("cannot create {}", cfg.out_dir), e);
    }
    let pairs_path = format!("{}/pairs.jsonl", cfg.out_dir);
    let out = File::create(&pairs_path)
        .unwrap_or_else(|e| io_error(&format!("cannot write {pairs_path}"), e));
    let mut w = BufWriter::new(out);
    for (a, b, s) in &pairs {
        let (ia, ib) = (
            &records[lsh_pos[*a as usize] as usize].id,
            &records[lsh_pos[*b as usize] as usize].id,
        );
        let (l, r) = if cmp_ids(ia, ib) != std::cmp::Ordering::Greater {
            (id_json(ia), id_json(ib))
        } else {
            (id_json(ib), id_json(ia))
        };
        if writeln!(
            w,
            "{{\"left_id\":{l},\"right_id\":{r},\"score\":{s:.6},\"score_kind\":\"minhash\"}}"
        )
        .is_err()
        {
            io_error(&format!("cannot write {pairs_path}"), "write failed");
        }
    }
    if w.flush().is_err() {
        io_error(&format!("cannot write {pairs_path}"), "flush failed");
    }

    let metrics = serde_json::json!({
        "tool": {"name": "revserp-gaoya", "version": VERSION},
        "library": {"name": "gaoya", "version": "0.2.2",
                    "license": "MIT", "repo": "https://github.com/serega/gaoya"},
        "config": {"threshold": cfg.threshold, "num_hashes": num_hashes, "bands": bands,
                   "band_width": rows, "shingle_words": cfg.shingle, "lowercase": cfg.lowercase,
                   "hash_bits": 32, "seed": cfg.seed,
                   "bands_source": if cfg.bands.is_some() { "explicit" } else { "calculate_minhash_params" }},
        "timing_ms": {"dataset_load": ms_load, "sign": ms_sign, "index_insert": ms_index,
                      "query_emit": ms_query, "match_total": ms_match},
        "peak_rss_kb": rss,
        "counts": {"processed": records.len(),
                   "skipped_empty_or_short": skipped, "lsh_indexed": lsh_pos.len(),
                   "accepted_pairs": pairs.len()},
        "notes": "all pairs from gaoya native MinHashIndex LSH + full-signature verify; score=minhash signature jaccard estimate, threshold applied to that estimate, NOT exact shingle jaccard; identical texts surface as minhash score 1.0, exact-content labeling left to evaluation"
    });
    let metrics_str =
        serde_json::to_string_pretty(&metrics).unwrap_or_else(|e| io_error("encoding metrics", e));
    let metrics_path = format!("{}/metrics.json", cfg.out_dir);
    if let Err(e) = std::fs::write(&metrics_path, metrics_str) {
        io_error(&format!("cannot write {metrics_path}"), e);
    }
    eprintln!(
        "revserp-gaoya: processed={} lsh_indexed={} pairs={} match_ms={} load_ms={} rss_kb={:?}",
        records.len(),
        lsh_pos.len(),
        pairs.len(),
        ms_match,
        ms_load,
        rss
    );
}
