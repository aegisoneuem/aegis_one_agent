use cmd
on-error continue
foreach host in *
  script cmd until BLOCK
    echo deliberate package failure on %COMPUTERNAME%
    exit /b 7
  BLOCK
end
