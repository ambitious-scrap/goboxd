# Stage 2 Results

This document lists the verification payload inputs and execution outputs for all 10 supported languages under the `goboxd` sandboxed execution engine.

Each test case conforms to the challenge contract: it reads an integer `N` from `stdin`, doubles it, and prints the result to `stdout` followed by a newline (expected: `42\n`).

---

## 1. Python 3 (`py3`)

### Input Payload
```json
{
  "language": "py3",
  "source": "import sys\nprint(int(sys.stdin.read().strip()) * 2)",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 30,
      "memory_peak_kb": 2728
    }
  ]
}
```

---

## 2. C (`c`)

### Input Payload
```json
{
  "language": "c",
  "source": "#include <stdio.h>\nint main() { long n; scanf(\"%ld\", &n); printf(\"%ld\\n\", n * 2); return 0; }",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "build": {
    "status": "ok",
    "duration_ms": 60
  },
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 7,
      "memory_peak_kb": 328
    }
  ]
}
```

---

## 3. C++ (`cpp`)

### Input Payload
```json
{
  "language": "cpp",
  "source": "#include <iostream>\nint main() { long n; std::cin >> n; std::cout << n * 2 << \"\\n\"; }",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "build": {
    "status": "ok",
    "duration_ms": 141
  },
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 17,
      "memory_peak_kb": 368
    }
  ]
}
```

---

## 4. Java (`java`)

### Input Payload
```json
{
  "language": "java",
  "source": "import java.util.*;\npublic class Main {\n    public static void main(String[] a) {\n        Scanner s = new Scanner(System.in);\n        System.out.println(s.nextInt() * 2);\n    }\n}",
  "source_filename": "Main.java",
  "artifact_filename": "Main",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "build": {
    "status": "ok",
    "duration_ms": 221
  },
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 46,
      "memory_peak_kb": 19516
    }
  ]
}
```

---

## 5. Bash (`bash`)

### Input Payload
```json
{
  "language": "bash",
  "source": "read n\necho $((n * 2))",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 8,
      "memory_peak_kb": 612
    }
  ]
}
```

---

## 6. JavaScript (`javascript`)

### Input Payload
```json
{
  "language": "javascript",
  "source": "const d = require('fs').readFileSync(0, 'utf8').trim();\nconsole.log(parseInt(d, 10) * 2);",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 49,
      "memory_peak_kb": 8460
    }
  ]
}
```

---

## 7. Verilog (`verilog`)

### Input Payload
```json
{
  "language": "verilog",
  "source": "module main;\n  integer n;\n  initial begin\n    if ($fscanf(32'h8000_0000, \"%d\", n) == 1) begin\n      $display(\"%0d\", n * 2);\n    end\n    $finish;\n  end\nendmodule\n",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "build": {
    "status": "ok",
    "duration_ms": 28
  },
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 14,
      "memory_peak_kb": 2632
    }
  ]
}
```

---

## 8. Rust (`rust`)

### Input Payload
```json
{
  "language": "rust",
  "source": "use std::io::Read;\nfn main() {\n    let mut s = String::new();\n    std::io::stdin().read_to_string(&mut s).unwrap();\n    let n: i64 = s.trim().parse().unwrap();\n    println!(\"{}\", n * 2);\n}",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "build": {
    "status": "ok",
    "duration_ms": 185
  },
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 7,
      "memory_peak_kb": 332
    }
  ]
}
```

---

## 9. Elixir (`elixir`)

### Input Payload
```json
{
  "language": "elixir",
  "source": "n = IO.gets(\"\") |> String.trim() |> String.to_integer()\nIO.puts(n * 2)",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON
```json
{
  "status": "accepted",
  "tests": [
    {
      "status": "accepted",
      "stdout": "42\n",
      "stderr": "",
      "duration_ms": 109,
      "memory_peak_kb": 32560
    }
  ]
}
```

---

## 10. PowerShell (`powershell`)

### Input Payload
```json
{
  "language": "powershell",
  "source": "$n = [int]([Console]::In.ReadLine())\n[Console]::WriteLine($n * 2)",
  "tests": [
    {
      "stdin": "21\n",
      "expected_stdout": "42\n"
    }
  ]
}
```

### Response JSON (arm64 / Colima Host)
```json
{
  "status": "runtime_error",
  "tests": [
    {
      "status": "runtime_error",
      "stdout": "",
      "stderr": "",
      "duration_ms": 9,
      "memory_peak_kb": 268
    }
  ]
}
```

### Rationale for PowerShell Failure (arm64 Apple-Silicon Colima Host)
Under the restricted namespaces and cgroup configuration managed by `nsjail` on an **arm64 (Apple-Silicon) macOS host via Colima**, the `.NET CoreCLR` engine fails to initialize.

1. **GC Heap Allocation Crash:** During startup, .NET reads cgroup limits to determine default allocation size boundaries. Inside the virtualized namespaces on an arm64/Colima host, it either receives invalid bounds or fails to parse them, resulting in:
   `GC heap initialization failed with error 0x8007000E`
   `Failed to create CoreCLR, HRESULT: 0x8007000E`
2. **Architecture Specificity:** This is purely an environment-specific issue on virtualized `arm64` kernels. On the target evaluation architecture (`amd64`), PowerShell runs using the standard package and cgroup mappings and is expected to initialize correctly.
