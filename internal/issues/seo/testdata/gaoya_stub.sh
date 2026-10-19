#!/bin/sh
# gaoya_stub.sh: fixture revserp-gaoya stand-in for error/cancel tests.
# Real-binary behavior is covered by smoke tests that resolve GAOYA_PATH/PATH
# explicitly. Modes via GAOYA_STUB_MODE:
#   pairs      emit one pair from GAOYA_STUB_PAIR="left:right:score" (numbers)
#   stringids  emit one pair with JSON string ids
#   malformed  emit a non-JSON pairs.jsonl row
#   crash      exit nonzero before writing output
#   hang       block until killed (cancellation tests; exec keeps the pid)
out_dir=""
while [ $# -gt 0 ]; do
  case "$1" in
    --out-dir) out_dir="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out_dir" || exit 1
mode="${GAOYA_STUB_MODE:-pairs}"
case "$mode" in
  pairs)
    pair="${GAOYA_STUB_PAIR:-0:1:0.95}"
    left="${pair%%:*}"; rest="${pair#*:}"; right="${rest%%:*}"; score="${rest#*:}"
    printf '{"left_id":%s,"right_id":%s,"score":%s,"score_kind":"minhash"}\n' \
      "$left" "$right" "$score" > "$out_dir/pairs.jsonl" || exit 1
    ;;
  stringids)
    pair="${GAOYA_STUB_PAIR:-0:1:0.95}"
    left="${pair%%:*}"; rest="${pair#*:}"; right="${rest%%:*}"; score="${rest#*:}"
    printf '{"left_id":"%s","right_id":"%s","score":%s,"score_kind":"minhash"}\n' \
      "$left" "$right" "$score" > "$out_dir/pairs.jsonl" || exit 1
    ;;
  malformed)
    printf 'this is not json\n' > "$out_dir/pairs.jsonl" || exit 1
    ;;
  noisy)
    pair="${GAOYA_STUB_PAIR:-0:1:0.95}"
    left="${pair%%:*}"; rest="${pair#*:}"; right="${rest%%:*}"; score="${rest#*:}"
    printf '{"left_id":%s,"right_id":%s,"score":%s,"score_kind":"minhash"}\n' \
      "$left" "$right" "$score" > "$out_dir/pairs.jsonl" || exit 1
    i=0
    while [ "$i" -lt 400 ]; do
      echo "gaoya-stub noise line $i padding padding padding padding padding" >&2
      i=$((i + 1))
    done
    ;;
  crash)
    exit 3
    ;;
  hang)
    exec sleep 120
    ;;
  *)
    echo "gaoya-stub: unknown GAOYA_STUB_MODE=$mode" >&2
    exit 2
    ;;
esac
printf '{"tool":{"name":"revserp-gaoya-stub"}}\n' > "$out_dir/metrics.json" || exit 1
