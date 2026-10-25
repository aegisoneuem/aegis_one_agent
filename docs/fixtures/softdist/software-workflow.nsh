use cmd
set capture off
on-error continue
foreach host in *
  log "package=${PACKAGE}, tag=${TAG}, target=${host}"
  script cmd until BLOCK
    echo package=${PACKAGE} tag=${TAG} host=%COMPUTERNAME%>C:\temp\AegisSoftDistTests\results\nsh.txt
    type C:\temp\AegisSoftDistTests\results\nsh.txt
  BLOCK
end
