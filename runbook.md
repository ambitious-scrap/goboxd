# goboxd — Demo Runbook

Validated run: 2026-06-12, colima (aarch64) + Docker, image `goboxd:demo`,
nsjail 3.4, cgroups v2 memory accounting **enabled**, seccomp `enforce`.

All commands below were executed end-to-end; results are summarized in
[Results](#results).

---

## 0. Environment (colima provides the Linux VM)

nsjail requires Linux + cgroup v2. On macOS, colima supplies the VM and Docker.

```sh
colima status                       # expect: running, runtime: docker
# cold start:  colima start --cpu 4 --memory 8
docker ps
```

### Clean slate — remove ALL containers + images (destructive)

```sh
docker rm -f $(docker ps -aq)       # stop + remove every container
docker rmi -f $(docker images -q)   # remove every image (bases re-pull on build)
docker ps -aq | wc -l ; docker images -q | wc -l   # both 0 = clean
```

### Build the image (named goboxd:demo)

```sh
# nsjail submodule must be present at tag 3.4 (Makefile verify-nsjail gate)
test -f external/nsjail/Makefile && git -C external/nsjail describe --tags   # -> 3.4
git submodule update --init --recursive    # if missing

docker build -t goboxd:demo .
```

### Run the container (map API 8080 + Prometheus 9090)

```sh
docker run -d --name goboxd-demo \
  --privileged --cgroupns=host \
  -p 8080:8080 -p 9090:9090 \
  goboxd:demo

# wait until ready
for i in $(seq 1 30); do
  [ "$(curl -s --max-time 2 localhost:8080/readyz | jq -r .status)" = ok ] && { echo ready; break; }
  sleep 1
done
```

`--cgroupns=host` is required: without it cgroup v2 `memory.max`/OOM accounting is
disabled and the sandbox silently falls back to `--rlimit_as` (no `memory_peak_kb`).

---

## 1. Capability probes

```sh
curl -s localhost:8080/healthz
curl -s localhost:8080/info   | jq -c '{ver:.build_info.version, nsjail:.nsjail.version, cgroups:.cgroups_enabled, langs:[.languages[].id]}'
curl -s localhost:8080/readyz | jq -c '{status, langs:(.languages|to_entries|map({(.key):.value.ok}))}'
```

7 languages: `bash c cpp java javascript py3 verilog`.

---

## 2. Verdict matrix

```sh
H='-H content-type:application/json'; B=localhost:8080/run

# accepted
curl -s $B $H -d '{"language":"py3","source":"n=int(input())\nprint(n*2)","tests":[{"stdin":"21\n","expected_stdout":"42\n"}]}'

# cpp build + multi-test
curl -s $B $H -d '{"language":"cpp","source":"#include<iostream>\nint main(){int a,b;std::cin>>a>>b;std::cout<<a+b<<\"\\n\";}","build":{"flags":["-O2","-std=c++17"]},"tests":[{"stdin":"2 3\n","expected_stdout":"5\n"},{"stdin":"10 20\n","expected_stdout":"30\n"}]}'

# wrong_output
curl -s $B $H -d '{"language":"py3","source":"print(99)","tests":[{"stdin":"","expected_stdout":"100\n"}]}'

# output_whitespace_mismatch  (expected lacks trailing newline)
curl -s $B $H -d '{"language":"py3","source":"print(12)","tests":[{"stdin":"","expected_stdout":"12"}]}'

# time_exceeded
curl -s $B $H -d '{"language":"py3","source":"while True: pass","run":{"limits":{"wall_time_s":2}},"tests":[{"stdin":"","expected_stdout":""}]}'

# memory_exceeded
curl -s $B $H -d '{"language":"py3","source":"x=bytearray(10**9)","run":{"limits":{"memory_kb":65536}},"tests":[{"stdin":"","expected_stdout":""}]}'

# runtime_error
curl -s $B $H -d '{"language":"py3","source":"raise SystemError(1)","tests":[{"stdin":"","expected_stdout":""}]}'

# build_failed
curl -s $B $H -d '{"language":"cpp","source":"int main(){ return ;","tests":[{"stdin":"","expected_stdout":""}]}'
```

---

## 3. Isolation

```sh
# outbound network blocked (network namespace) -> "BLOCKED OSError"
curl -s $B $H -d '{"language":"py3","source":"import socket\ntry:\n socket.socket().connect((\"1.1.1.1\",80))\n print(\"OPEN\")\nexcept Exception as e:\n print(\"BLOCKED\",type(e).__name__)","tests":[{"stdin":"","expected_stdout":""}]}'

# memory peak reported from cgroup
curl -s $B $H -d '{"language":"py3","source":"print(sum(range(1000)))","tests":[{"stdin":"","expected_stdout":"499500\n"}]}' | jq '.tests[0].memory_peak_kb'
```

Note: reading `/etc/shadow` inside the jail returns the jail's **own** minimal
rootfs copy (locked accounts, no secrets) — not the host file.

---

## 4. Compiled languages

Java and Verilog use `from_request` filename strategy — caller MUST supply both
`source_filename` and `artifact_filename`. Java run cmd is `java {{artifact}}`,
so `artifact_filename` is the class name (no `.class`).

```sh
# bash
curl -s $B $H -d '{"language":"bash","source":"read x; echo $((x+1))","tests":[{"stdin":"41\n","expected_stdout":"42\n"}]}'

# java
curl -s $B $H -d '{"language":"java","source_filename":"Main.java","artifact_filename":"Main","source":"public class Main{public static void main(String[] a){System.out.println(\"hi\");}}","tests":[{"stdin":"","expected_stdout":"hi\n"}]}'

# javascript
curl -s $B $H -d '{"language":"javascript","source":"console.log(2+2)","tests":[{"stdin":"","expected_stdout":"4\n"}]}'

# verilog
curl -s $B $H -d '{"language":"verilog","source_filename":"top.v","artifact_filename":"top.vvp","source":"module top; initial begin $display(\"v\"); $finish; end endmodule","tests":[{"stdin":"","expected_stdout":"v\n"}]}'
```

---

## 5. Admission control (C-1) — load shedding

```sh
H='-H content-type:application/json'; B=localhost:8080/run
for i in $(seq 1 20); do
  curl -s -o /dev/null -w "%{http_code} " $B $H \
    -d '{"language":"py3","source":"while True:pass","run":{"limits":{"wall_time_s":8}},"tests":[{"stdin":"","expected_stdout":""}]}' &
done; wait; echo
# observed: 8x 503 (shed) + 12x 200
```

---

## 6. Artifact cache (C-2)

```sh
# heavy build, unique source, run twice -> 2nd is a cache hit (build replayed)
SRC='#include<iostream>\ntemplate<int N>struct F{enum{v=F<N-1>::v+F<N-2>::v};};template<>struct F<0>{enum{v=0};};template<>struct F<1>{enum{v=1};};int main(){std::cout<<F<40>::v<<"\\n";}'
for i in 1 2; do
  curl -s $B $H -d "{\"language\":\"cpp\",\"source\":\"$SRC\",\"artifact_filename\":\"a.out\",\"build\":{\"flags\":[\"-O2\"]},\"tests\":[{\"stdin\":\"\",\"expected_stdout\":\"102334155\\n\"}]}" \
    | jq -c "{run:$i, build_ms:.build.duration_ms}"
done

docker exec goboxd-demo ls /tmp/goboxd-cache   # content-addressed entries
```

Cache hit confirmed via metric `goboxd_cache_hits_total{language="cpp"} 1`.

---

## 7. Prometheus metrics (port 9090)

```sh
# all goboxd metrics
curl -s localhost:9090/metrics | grep -E '^goboxd_' | grep -v '#'

# key counters only
curl -s localhost:9090/metrics | grep -E '^goboxd_(runs_total|requests_total|rejected_total|cache_(hits|misses)_total|inflight|queue_depth)' | sort
```

Sample after this run:

```
goboxd_requests_total              28
goboxd_rejected_total              8        # == 8x 503 from load-shed test
goboxd_cache_hits_total{cpp}       1        # cache replay confirmed
goboxd_cache_misses_total{cpp}     3
goboxd_inflight                    0
goboxd_queue_depth                 0
goboxd_internal_errors_total       0
goboxd_runs_total{py3,time_exceeded}            13
goboxd_runs_total{py3,memory_exceeded}          1
goboxd_runs_total{py3,output_whitespace_mismatch} 1
goboxd_runs_total{py3,runtime_error}            1
goboxd_runs_total{py3,wrong_output}             2
goboxd_runs_total{cpp,accepted}                 3
goboxd_runs_total{cpp,build_failed}             1
goboxd_runs_total{java,accepted}                1
goboxd_runs_total{javascript,accepted}          1
goboxd_runs_total{verilog,accepted}             1
goboxd_runs_total{bash,accepted}                1
```

Dual-lane queue split visible in `goboxd_queue_wait_seconds{lane="heavy|light"}`.

---

## 8. Teardown

```sh
docker rm -f goboxd-demo
docker rmi goboxd:demo
```

---

## Results

| Area | Test | Result |
|------|------|--------|
| Health | healthz/readyz/info | 7 langs `ok`, nsjail 3.4, cgroups on |
| Verdict | accepted (py/cpp/bash/java/js/verilog) | ✅ |
| Verdict | wrong_output | ✅ |
| Verdict | output_whitespace_mismatch | ✅ |
| Verdict | time_exceeded | ✅ |
| Verdict | memory_exceeded | ✅ |
| Verdict | runtime_error | ✅ |
| Verdict | build_failed | ✅ |
| Isolation | outbound network blocked | ✅ BLOCKED OSError |
| Isolation | memory_peak_kb from cgroup | ✅ (~2.9 MB) |
| C-1 | 20 parallel → 8×503 + 12×200 | ✅ matches `rejected_total 8` |
| C-2 | cache hit on identical build | ✅ `cache_hits_total{cpp} 1` |
| Metrics | Prometheus on :9090 | ✅ all counters populated |

### Gotchas
- **java**: needs `source_filename:"Main.java"` + `artifact_filename:"Main"`.
- **verilog**: needs `source_filename:"top.v"` + `artifact_filename:"top.vvp"`.
- **whitespace**: `expected_stdout` is exact-byte; add trailing `\n` or get
  `output_whitespace_mismatch`.
- **9090** is a separate admin port — must be `-p 9090:9090` (image only EXPOSEs 8080).
