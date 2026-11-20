# PowerShell 入口：转给 Git Bash 执行同名脚本。
# 用法：.\deploy\local\bike.ps1 up | down | gen-env | verify | gateway [start|stop|validate] | run <模块>
param([Parameter(Mandatory = $true)][string]$Command, [Parameter(ValueFromRemainingArguments = $true)][string[]]$Rest)

$valid = 'up', 'down', 'gen-env', 'verify', 'gateway', 'run'
if ($valid -notcontains $Command) { Write-Error "用法：bike.ps1 $($valid -join ' | ')"; exit 2 }

$bash = @(
  (Join-Path $env:ProgramFiles 'Git\bin\bash.exe'),
  (Join-Path ${env:ProgramFiles(x86)} 'Git\bin\bash.exe')
) | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
if (-not $bash) { Write-Error '没有找到 Git Bash（Git for Windows），请先安装'; exit 2 }

$script = Join-Path $PSScriptRoot "$Command.sh"
& $bash $script @Rest
exit $LASTEXITCODE
