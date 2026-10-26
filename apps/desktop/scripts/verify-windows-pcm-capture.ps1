param(
    [string]$CapturePath = (Join-Path $PSScriptRoot '../resources/dictation/windows-pcm-capture.cs'),
    [string]$HelperPath = (Join-Path $PSScriptRoot '../resources/dictation/windows-speech.ps1')
)
$ErrorActionPreference = "Stop"
$captureSource = Get-Content -LiteralPath $CapturePath -Raw
$helperScript = Get-Content -LiteralPath $HelperPath -Raw
# Compile the exact C# used by the packaged helper, replacing only external APIs.
$speechSource = [regex]::Match($helperScript, '(?s)\$speechSource = @''\r?\n(.*?)\r?\n''@').Groups[1].Value
if (-not $speechSource) { throw 'Could not extract production Windows helper' }
$fixture = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'windows-dictation-fixture.cs') -Raw
# Move the fixture's using directives ahead of the production compilation units.
$fixtureUsings = [regex]::Matches($fixture, '(?m)^using [^;]+;') | ForEach-Object { $_.Value }
$fixtureBody = [regex]::Replace($fixture, '(?m)^using [^;]+;\r?\n', '')
# Avoid duplicate using warnings, treated as errors by Add-Type on some hosts.
$speechBody = [regex]::Replace($speechSource, '(?m)^using [^;]+;\r?\n', '')
$speechUsings = [regex]::Matches($speechSource, '(?m)^using [^;]+;') | ForEach-Object { $_.Value }
$usings = (@($fixtureUsings) + @($speechUsings) | Select-Object -Unique) -join [Environment]::NewLine
Add-Type -TypeDefinition ($usings + [Environment]::NewLine + $speechBody + [Environment]::NewLine + $captureSource + [Environment]::NewLine + $fixtureBody)
[WindowsDictationRegression]::Run()
Write-Output 'Production Windows Stop, release, late buffers and acknowledgement ordering: OK'
$buffer = New-Object MulticaDictationNative.DictationPcmBuffer
$before = [Text.Encoding]::UTF8.GetBytes('before stop')
$after = [Text.Encoding]::UTF8.GetBytes('after stop')
if (-not $buffer.Append($before)) { throw 'Pre-stop samples were rejected' }
$snapshot = $buffer.Seal()
if ($buffer.Append($after)) { throw 'Post-stop samples were accepted' }
if ([Text.Encoding]::UTF8.GetString($snapshot) -ne 'before stop') { throw 'Transcript input contains post-stop samples' }
if ([Text.Encoding]::UTF8.GetString($buffer.Seal()) -ne 'before stop') { throw 'Sealed input changed after stop' }
$bounded = New-Object MulticaDictationNative.DictationPcmBuffer
$null = $bounded.Append((New-Object byte[] (16000 * 2 * 121)))
if ($bounded.Seal().Length -ne (16000 * 2 * 120)) { throw 'Capture exceeded its memory limit' }
Write-Output 'PCM stop boundary and memory limit: OK'
