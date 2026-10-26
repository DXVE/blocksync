# 本地演练：用合成文件完整模拟「本机 -> 移动硬盘 -> 另一端」的双向增量同步，逐项断言。
# 用法：右键「使用 PowerShell 运行」，或 powershell -ExecutionPolicy Bypass -File 模拟测试.ps1
#     可选参数： -FileMB 512 -FileCount 3 -ChangeMB 8（想更接近真实规模就调大 FileMB）
param(
    [string]$Root = (Join-Path $env:TEMP 'blocksync-rehearsal'),
    [int]$FileMB = 256,
    [int]$FileCount = 3,
    [int]$ChangeMB = 8
)

$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false } catch { }

$exe = Join-Path $PSScriptRoot 'dist\blocksync.exe'
if (-not (Test-Path $exe)) { throw "找不到 $exe" }

$A = Join-Path $Root 'A'   # 模拟本机源目录
$D = Join-Path $Root 'D'   # 模拟移动硬盘
$B = Join-Path $Root 'B'   # 模拟另一端

if (Test-Path $Root) {
    if ($Root -notlike '*blocksync-rehearsal*') { throw "拒绝清理非演练目录: $Root" }
    Remove-Item $Root -Recurse -Force
}
foreach ($p in @($A, $D, $B)) { New-Item -ItemType Directory -Path $p -Force | Out-Null }

$script:failed = 0
function Check([string]$name, [bool]$ok, [string]$detail = '') {
    if ($ok) {
        Write-Host "  [通过] $name" -ForegroundColor Green
    } else {
        Write-Host "  [失败] $name  $detail" -ForegroundColor Red
        $script:failed++
    }
}

function Invoke-Exe([string[]]$argv) {
    $ErrorActionPreference = 'Continue'
    $out = (& $exe @argv 2>&1 | Out-String)
    $code = $LASTEXITCODE
    $ErrorActionPreference = 'Stop'
    if ($code -ne 0) { throw "blocksync 失败(退出码 $code):`n$out" }
    return $out
}

function New-Blob([string]$path, [int]$mb, [int]$seed) {
    $buf = New-Object byte[] (1MB)
    $rnd = [System.Random]::new($seed)
    $fs = [System.IO.File]::Create($path)
    try { for ($i = 0; $i -lt $mb; $i++) { $rnd.NextBytes($buf); $fs.Write($buf, 0, $buf.Length) } }
    finally { $fs.Close() }
}

# 原地分散改写、长度不变——模拟程序运行时对大文件的写入模式
function Edit-InPlace([string]$path, [int64]$bytes, [System.Random]$rnd) {
    $size = (Get-Item $path).Length
    $fs = [System.IO.File]::Open($path, 'Open', 'ReadWrite')
    $buf = New-Object byte[] 32768
    $changed = 0
    try {
        while ($changed -lt $bytes) {
            $len = 4096 + $rnd.Next(28000)
            if ($changed + $len -gt $bytes) { $len = [int]($bytes - $changed) }
            $off = [int64]($rnd.NextDouble() * ($size - 65536))
            $rnd.NextBytes($buf)
            $fs.Seek($off, 'Begin') | Out-Null
            $fs.Write($buf, 0, $len)
            $changed += $len
        }
    } finally { $fs.Close() }
    return $changed
}

function Get-Tree([string]$root) {
    $map = @{}
    Get-ChildItem -Path $root -Recurse -File -Force | ForEach-Object {
        $rel = $_.FullName.Substring($root.Length + 1).Replace('\', '/')
        if ($rel -like '.blocksync/*') { return }
        $map[$rel] = (Get-FileHash -Algorithm SHA256 -Path $_.FullName).Hash
    }
    return $map
}

function Compare-Trees([string]$x, [string]$y) {
    $mx = Get-Tree $x
    $my = Get-Tree $y
    if ($mx.Count -ne $my.Count) { return "文件数不同: $($mx.Count) vs $($my.Count)" }
    foreach ($k in $mx.Keys) {
        if (-not $my.ContainsKey($k)) { return "缺少 $k" }
        if ($mx[$k] -ne $my[$k]) { return "$k 内容不同" }
    }
    return ''
}

function Parse-Bytes([string]$text, [string]$label) {
    $m = [regex]::Match($text, "$label\s+([\d.]+)\s*(B|KiB|MiB|GiB)")
    if (-not $m.Success) { return -1 }
    $v = [double]$m.Groups[1].Value
    switch ($m.Groups[2].Value) {
        'B' { $v }
        'KiB' { $v * 1KB }
        'MiB' { $v * 1MB }
        'GiB' { $v * 1GB }
    }
}

function Sync([string]$src, [string]$dst, [string[]]$extra = @()) {
    # 演练是无人值守的，用 --yes 跳过首次同步与删除前的确认
    return Invoke-Exe (@('sync', $src, $dst, '--quiet', '--yes') + $extra)
}

$totalMB = $FileMB * $FileCount
Write-Host ""
Write-Host "演练目录: $Root" -ForegroundColor Cyan
Write-Host "数据规模: $FileCount 个 x $FileMB MiB = $totalMB MiB" -ForegroundColor Cyan
if ($totalMB -gt 2048) {
    Write-Host "提示: 脚本每步都做全树 SHA256 比对，规模越大越慢；想看大数字建议 -FileCount 2 -FileMB 1024。" -ForegroundColor Yellow
}

# ---------- 0 ----------
Write-Host ""
Write-Host "== 0. 生成模拟源目录 =="
for ($i = 1; $i -le $FileCount; $i++) {
    New-Blob (Join-Path $A ("part-00{0}.bin" -f $i)) $FileMB (100 + $i)
}
Set-Content -Path (Join-Path $A 'app.ini') -Value "config.version = `"8`"`ndisplayName = `"rehearsal`"" -NoNewline
Write-Host "  已生成 $totalMB MiB + app.ini"

# ---------- 1 ----------
Write-Host ""
Write-Host "== 1. 首次全量投递 A -> D -> B =="
$o1 = Sync $A $D
$null = Sync $D $B
$n1 = [regex]::Match($o1, '改动\s+(\d+)\s+个').Groups[1].Value
$d1 = Compare-Trees $A $D
$d2 = Compare-Trees $D $B
Check "首轮复制了全部文件" ([int]$n1 -eq ($FileCount + 1)) "改动 $n1 个，期望 $($FileCount + 1)"
Check "A 与 D 内容一致" ($d1 -eq '') $d1
Check "D 与 B 内容一致" ($d2 -eq '') $d2

# ---------- 2 ----------
Write-Host ""
Write-Host "== 2. 无改动重复同步 =="
$o = Sync $A $D
Check "报告已是最新" ($o -match '已是最新') $o.Trim()
Check "不产生任何写入" ((Parse-Bytes $o '写入') -le 0) $o.Trim()

# ---------- 3 ----------
Write-Host ""
Write-Host "== 3. 模拟程序运行：分散原地改写约 $ChangeMB MiB =="
$rnd = [System.Random]::new(7)
$changed = 0
$perFile = [int64]($ChangeMB * 1MB / $FileCount)
Get-ChildItem -Path $A -Filter *.bin | ForEach-Object {
    $changed += Edit-InPlace $_.FullName $perFile $rnd
}
Write-Host ("  实际改写 {0:N1} MiB" -f ($changed / 1MB))
$o = Sync $A $D
$w = Parse-Bytes $o '写入'
$d3 = Compare-Trees $A $D
Check "A -> D 只写变更块" ($w -gt 0 -and $w -lt ($changed * 3)) ("写入 {0:N1} MiB / 实际改动 {1:N1} MiB" -f ($w/1MB), ($changed/1MB))
Check "远小于全量" ($w -lt ($totalMB * 1MB * 0.2)) ("写入 {0:N1} MiB，全量为 {1} MiB" -f ($w/1MB), $totalMB)
Check "A 与 D 内容一致" ($d3 -eq '') $d3

# ---------- 4 ----------
Write-Host ""
Write-Host "== 4. 取回方向 D -> B（应零读取） =="
$o = Sync $D $B
$r = Parse-Bytes $o '读取'
$w2 = Parse-Bytes $o '写入'
$d4 = Compare-Trees $D $B
Check "不重读整盘" ($r -eq 0) ("读取 {0:N0} 字节" -f $r)
Check "写入量与上一跳相当" ($w2 -gt 0 -and $w2 -lt ($changed * 3)) ("写入 {0:N1} MiB" -f ($w2/1MB))
Check "D 与 B 内容一致" ($d4 -eq '') $d4

# ---------- 5 ----------
Write-Host ""
Write-Host "== 5. 反向：在 B 改动后回传 B -> D -> A =="
$null = Edit-InPlace (Join-Path $B 'part-001.bin') ([int64](2MB)) ([System.Random]::new(11))
Set-Content -Path (Join-Path $B 'app.ini') -Value "config.version = `"8`"`ndisplayName = `"edited-at-B`"" -NoNewline
$null = Sync $B $D
$null = Sync $D $A
$d5 = Compare-Trees $B $D
$d6 = Compare-Trees $D $A
Check "B 与 D 内容一致" ($d5 -eq '') $d5
Check "D 与 A 内容一致" ($d6 -eq '') $d6

# ---------- 6 ----------
Write-Host ""
Write-Host "== 6. 新增与删除 =="
New-Blob (Join-Path $A 'new-part.bin') 64 555
Remove-Item (Join-Path $A ("part-00{0}.bin" -f $FileCount))
$null = Sync $A $D
$null = Sync $D $B
$d7 = Compare-Trees $A $B
Check "新增文件已同步" (Test-Path (Join-Path $B 'new-part.bin'))
Check "删除的文件已移除" (-not (Test-Path (Join-Path $B ("part-00{0}.bin" -f $FileCount))))
Check "A 与 B 内容一致" ($d7 -eq '') $d7

# ---------- 7 ----------
Write-Host ""
Write-Host "== 7. 输出目录被外部破坏后应能修复 =="
$victim = Join-Path $D 'part-001.bin'
$fs = [System.IO.File]::Open($victim, 'Open', 'ReadWrite')
$fs.Seek(12345, 'Begin') | Out-Null
$fs.WriteByte(0x00)
$fs.Close()
$null = Sync $A $D
$d8 = Compare-Trees $A $D
Check "检测到并修复" ($d8 -eq '') $d8

# ---------- 8 ----------
Write-Host ""
Write-Host "== 8. 文件截断与追加 =="
$p = Join-Path $A 'new-part.bin'
$len = (Get-Item $p).Length
$fs = [System.IO.File]::Open($p, 'Open', 'ReadWrite')
$fs.SetLength([int64]($len - 5MB))
$fs.Seek(0, 'End') | Out-Null
$buf = New-Object byte[] (3MB)
[System.Random]::new(3).NextBytes($buf)
$fs.Write($buf, 0, $buf.Length)
$fs.Close()
$null = Sync $A $D
$null = Sync $D $B
$d9 = Compare-Trees $A $B
Check "A 与 B 内容一致（含长度变化）" ($d9 -eq '') $d9

# ---------- 9 ----------
Write-Host ""
Write-Host "== 9. 内置 verify =="
$va = Invoke-Exe @('verify', $A, '--quiet')
$vb = Invoke-Exe @('verify', $B, '--quiet')
Check "A 侧索引校验通过" ($va -match '校验通过') $va.Trim()
Check "B 侧索引校验通过" ($vb -match '校验通过') $vb.Trim()

# ---------- 10 ----------
Write-Host ""
Write-Host "== 10. dry-run 不写盘 =="
$before = Get-Tree $D
$null = Sync $A $D @('--dry-run')
$after = Get-Tree $D
$same = ($before.Count -eq $after.Count)
foreach ($k in $before.Keys) { if ($after[$k] -ne $before[$k]) { $same = $false } }
Check "dry-run 后输出目录未变" $same ''

Write-Host ""
if ($script:failed -eq 0) {
    Write-Host "全部通过（共 10 组检查）。可在正式目录上执行同步。" -ForegroundColor Green
    Write-Host "演练数据保留在: $Root  可随时删除。"
    exit 0
} else {
    Write-Host "有 $($script:failed) 项未通过，请先排查后再操作正式目录。" -ForegroundColor Red
    exit 1
}
