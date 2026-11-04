!include "FileFunc.nsh"
!include "LogicLib.nsh"

; Keep an explicit /D= selection and existing installation path. For a fresh
; installation without either, use the NekoSSH directory on the C: drive.
!macro customInit
  ReadRegStr $0 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.neko233.terminal233" "InstallLocation"
  ${GetParameters} $1
  ClearErrors
  ${GetOptions} $1 "/D=" $2
  ${If} ${Errors}
    ${If} $0 == ""
      StrCpy $INSTDIR "C:\App\nekossh"
    ${EndIf}
  ${EndIf}
!macroend
