# 本地演示：启动 mockllm，然后跑流式回答 / 工具调用 / 结构化输出。
# 用法：powershell -ExecutionPolicy Bypass -File .\scripts\demo.ps1
$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $false
$Root = Split-Path -Parent $PSScriptRoot
Push-Location $Root
$mock = $null
try {
    New-Item -ItemType Directory -Force bin | Out-Null
    go build -o bin\mockllm.exe ./cmd/mockllm
    if ($LASTEXITCODE -ne 0) { throw "build mockllm failed" }
    go build -o bin\agent.exe ./cmd/agent
    if ($LASTEXITCODE -ne 0) { throw "build agent failed" }

    $mock = Start-Process -FilePath (Join-Path $Root 'bin\mockllm.exe') `
        -ArgumentList '-addr', '127.0.0.1:18080' -PassThru -WindowStyle Hidden

    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18080/health' -TimeoutSec 1 | Out-Null
            $ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 200
        }
    }
    if (-not $ready) { throw "mockllm 未启动：端口 18080 可能被占用" }

    $common = @('--base-url', 'http://127.0.0.1:18080/v1', '--api-key', 'mock')

    Write-Host "`n=== 1. 流式回答 ===" -ForegroundColor Cyan
    & .\bin\agent.exe @common --no-tools "用一句话介绍你自己"

    Write-Host "`n=== 2. 工具调用：get_current_time ===" -ForegroundColor Cyan
    & .\bin\agent.exe @common "现在几点？"

    Write-Host "`n=== 3. 结构化输出：JSON Schema 校验 ===" -ForegroundColor Cyan
    & .\bin\agent.exe @common --schema .\schema\person.schema.json "提取：Ada 36 岁，住在上海"

    Write-Host "`n=== 4. 断网优雅报错 ===" -ForegroundColor Cyan
    & .\bin\agent.exe --base-url http://127.0.0.1:1 --api-key mock --max-retries 0 --color never "hi"
    Write-Host "(exit code: $LASTEXITCODE，预期为 1)" -ForegroundColor DarkGray
    $LASTEXITCODE = 0
} finally {
    if ($mock -and -not $mock.HasExited) {
        Stop-Process -Id $mock.Id -Force -ErrorAction SilentlyContinue
    }
    Pop-Location
}
exit 0
