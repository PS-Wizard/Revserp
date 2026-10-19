use std::io::Write;
use std::process::Command;

fn bin() -> String {
    env!("CARGO_BIN_EXE_revserp-gaoya").to_string()
}

fn run(args: &[&str]) -> std::process::Output {
    Command::new(bin())
        .args(args)
        .output()
        .expect("run test binary")
}

fn tmpdir(name: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "revserp-gaoya-test-{}-{}",
        name,
        std::process::id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

#[test]
fn duplicate_pair_found_unrelated_not_paired() {
    let dir = tmpdir("dup");
    let input = dir.join("in.jsonl");
    let out = dir.join("out");
    let text_a = "the quick brown fox jumps over the lazy dog near the river bank every morning";
    let mut f = std::fs::File::create(&input).unwrap();
    writeln!(
        f,
        "{{\"id\": 1, \"text\": {}}}",
        serde_json::to_string(text_a).unwrap()
    )
    .unwrap();
    writeln!(
        f,
        "{{\"id\": 2, \"text\": {}}}",
        serde_json::to_string(text_a).unwrap()
    )
    .unwrap();
    writeln!(
        f,
        "{{\"id\": 3, \"text\": \"quantum entanglement photon polarization superconductors unrelated topic\"}}"
    )
    .unwrap();

    let o = run(&[
        "--input",
        input.to_str().unwrap(),
        "--out-dir",
        out.to_str().unwrap(),
    ]);
    assert!(
        o.status.success(),
        "stderr: {}",
        String::from_utf8_lossy(&o.stderr)
    );

    let pairs = std::fs::read_to_string(out.join("pairs.jsonl")).unwrap();
    let lines: Vec<&str> = pairs.lines().filter(|l| !l.trim().is_empty()).collect();
    assert_eq!(lines.len(), 1, "expected exactly one pair, got: {pairs}");
    let v: serde_json::Value = serde_json::from_str(lines[0]).unwrap();
    assert_eq!(v["left_id"], "1");
    assert_eq!(v["right_id"], "2");
    assert_eq!(v["score_kind"], "minhash");
    assert!(v["score"].as_f64().unwrap() > 0.9);
    assert!(std::path::Path::new(&out.join("metrics.json")).exists());
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn invalid_cli_and_input_fail_nonzero() {
    let o = run(&["--bogus"]);
    assert_eq!(o.status.code(), Some(2));

    let o = run(&["--input", "only-one"]);
    assert_eq!(o.status.code(), Some(2));

    let dir = tmpdir("badflag");
    let o = run(&[
        "--input",
        "x",
        "--out-dir",
        dir.to_str().unwrap(),
        "--threshold",
        "7",
    ]);
    assert_eq!(o.status.code(), Some(2));

    let missing = dir.join("nope.jsonl").to_string_lossy().into_owned();
    let outdir = dir.to_string_lossy().into_owned();
    let o = run(&["--input", missing.as_str(), "--out-dir", outdir.as_str()]);
    assert_eq!(o.status.code(), Some(1));

    let bad = dir.join("bad.jsonl");
    std::fs::write(&bad, "this is not json\n").unwrap();
    let o = run(&[
        "--input",
        bad.to_str().unwrap(),
        "--out-dir",
        dir.join("o").to_str().unwrap(),
    ]);
    assert_eq!(o.status.code(), Some(1));

    let noid = dir.join("noid.jsonl");
    std::fs::write(&noid, "{\"text\": \"hello world foo bar baz\"}\n").unwrap();
    let o = run(&[
        "--input",
        noid.to_str().unwrap(),
        "--out-dir",
        dir.join("o2").to_str().unwrap(),
    ]);
    assert_eq!(o.status.code(), Some(1));
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn version_and_help_exit_zero() {
    let o = run(&["--version"]);
    assert!(o.status.success());
    assert!(String::from_utf8_lossy(&o.stdout).contains("revserp-gaoya"));

    let o = run(&["--help"]);
    assert!(o.status.success());
    assert!(String::from_utf8_lossy(&o.stdout).contains("--threshold"));
}

#[test]
fn tricky_ids_stay_valid_json_strings() {
    let dir = tmpdir("ids");
    let input = dir.join("in.jsonl");
    let out = dir.join("out");
    let text = "the quick brown fox jumps over the lazy dog near the river bank every morning";
    let mut f = std::fs::File::create(&input).unwrap();
    for id in ["001", "+1", "quote\"back\\slash", "plain"] {
        let line = serde_json::json!({"id": id, "text": text}).to_string();
        use std::io::Write as _;
        writeln!(f, "{line}").unwrap();
    }
    drop(f);

    let o = run(&[
        "--input",
        input.to_str().unwrap(),
        "--out-dir",
        out.to_str().unwrap(),
    ]);
    assert!(
        o.status.success(),
        "stderr: {}",
        String::from_utf8_lossy(&o.stderr)
    );

    let mut seen: Vec<String> = Vec::new();
    for line in std::fs::read_to_string(out.join("pairs.jsonl"))
        .unwrap()
        .lines()
    {
        if line.trim().is_empty() {
            continue;
        }
        let v: serde_json::Value = serde_json::from_str(line).unwrap();
        for k in ["left_id", "right_id"] {
            assert!(v[k].is_string(), "id must be a JSON string: {line}");
            seen.push(v[k].as_str().unwrap().to_string());
        }
    }
    for id in ["001", "+1", "quote\"back\\slash", "plain"] {
        assert!(
            seen.contains(&id.to_string()),
            "id {id:?} missing from pairs"
        );
    }
    let raw = std::fs::read_to_string(out.join("pairs.jsonl")).unwrap();
    assert!(!raw.contains(":001"), "raw numeric id emitted: {raw}");
    assert!(!raw.contains(":+1"), "raw numeric id emitted: {raw}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn strict_schema_errors() {
    let dir = tmpdir("strict");
    let out = || {
        dir.join(format!(
            "o{}",
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ))
    };

    let p = dir.join("missing-text.jsonl");
    std::fs::write(
        &p,
        serde_json::json!({"id": "a", "nope": 1}).to_string() + "\n",
    )
    .unwrap();
    let o = run(&[
        "--input",
        p.to_str().unwrap(),
        "--out-dir",
        out().to_str().unwrap(),
    ]);
    assert_eq!(
        o.status.code(),
        Some(1),
        "stderr: {}",
        String::from_utf8_lossy(&o.stderr)
    );

    let p = dir.join("num-text.jsonl");
    std::fs::write(&p, "{\"id\": \"a\", \"text\": 42}\n").unwrap();
    let o = run(&[
        "--input",
        p.to_str().unwrap(),
        "--out-dir",
        out().to_str().unwrap(),
    ]);
    assert_eq!(o.status.code(), Some(1));

    let p = dir.join("empty-text.jsonl");
    std::fs::write(&p, "{\"id\": \"a\", \"text\": \"\"}\n").unwrap();
    let o = run(&[
        "--input",
        p.to_str().unwrap(),
        "--out-dir",
        out().to_str().unwrap(),
    ]);
    assert_eq!(
        o.status.code(),
        Some(0),
        "stderr: {}",
        String::from_utf8_lossy(&o.stderr)
    );

    let p = dir.join("dup-id.jsonl");
    std::fs::write(
        &p,
        "{\"id\": \"a\", \"text\": \"hello world foo bar test one\"}\n{\"id\": \"a\", \"text\": \"hello world foo bar test two\"}\n",
    )
    .unwrap();
    let o = run(&[
        "--input",
        p.to_str().unwrap(),
        "--out-dir",
        out().to_str().unwrap(),
    ]);
    assert_eq!(
        o.status.code(),
        Some(1),
        "stderr: {}",
        String::from_utf8_lossy(&o.stderr)
    );

    let p = dir.join("bands.jsonl");
    std::fs::write(&p, "{\"id\": \"a\", \"text\": \"hello world\"}\n").unwrap();
    let o = run(&[
        "--input",
        p.to_str().unwrap(),
        "--out-dir",
        out().to_str().unwrap(),
        "--bands",
        "64",
        "--band-width",
        "4",
        "--num-hashes",
        "128",
    ]);
    assert_eq!(o.status.code(), Some(2));

    let o = run(&[
        "--input",
        p.to_str().unwrap(),
        "--out-dir",
        out().to_str().unwrap(),
        "--bands",
        "16",
    ]);
    assert_eq!(o.status.code(), Some(2));
    let _ = std::fs::remove_dir_all(&dir);
}
