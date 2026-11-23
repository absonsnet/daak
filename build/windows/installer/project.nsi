Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them with the values from ProjectInfo.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
##
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
####
## The following information is taken from the ProjectInfo file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "MyProject" # Default "{{.Name}}"
## !define INFO_COMPANYNAME    "MyCompany" # Default "{{.Info.CompanyName}}"
## !define INFO_PRODUCTNAME    "MyProduct" # Default "{{.Info.ProductName}}"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "{{.Info.ProductVersion}}"
## !define INFO_COPYRIGHT      "Copyright" # Default "{{.Info.Copyright}}"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
####

## No UAC prompt at launch: the installer always starts unelevated and only
## elevates when the user picks "install for all users" (Daak.InstallModeLeave
## relaunches it elevated). wails_tools.nsh emits RequestExecutionLevel from
## this define; MultiUser.nsh emits the same "user" level for Standard, and
## makensis accepts the duplicate directive.
!define REQUEST_EXECUTION_LEVEL "user"

####
## Include the wails tools
####
!include "wails_tools.nsh"

## Multi-user install: per-user by default, all-users on request.
## - MULTIUSER_EXECUTIONLEVEL Standard: RequestExecutionLevel user, so the
##   installer never asks for admin rights up front.
## - MULTIUSER_INSTALLMODE_COMMANDLINE: /AllUsers and /CurrentUser switches,
##   used by the elevation relaunch (see Daak.InstallModeLeave).
## - MULTIUSER_INSTALLMODE_INSTDIR: default folder name below the mode's
##   base folder (Program Files for all-users, %LOCALAPPDATA%\Programs for
##   per-user). The previous install's folder wins over the default — see
##   Daak.DetectPreviousInstallMode.
!define MULTIUSER_EXECUTIONLEVEL Standard
!define MULTIUSER_INSTALLMODE_COMMANDLINE
!define MULTIUSER_INSTALLMODE_INSTDIR "Daak"
!define MULTIUSER_USE_PROGRAMFILES64
!include "MultiUser.nsh"
!include "nsDialogs.nsh"
!include "LogicLib.nsh"

## Named auto-reset event the installer signals to ask a running Daak to quit
## gracefully (created by the app, internal/platform QuitEventName).
## Must stay in sync with the app side.
!define DAAK_QUIT_EVENT "Local\DaakQuit"
## Seconds to wait for Daak to exit after signalling the quit event.
!define DAAK_QUIT_WAIT_SECONDS 15

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!define MUI_FINISHPAGE_RUN_TEXT "Run Daak"
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

## Write the mail-client registration under the given registry root
## (HKLM for all-users, HKCU for per-user).
!macro daak.writeMailRegistration ROOT
  WriteRegStr ${ROOT} "SOFTWARE\Clients\Mail\${INFO_PRODUCTNAME}" "" "${INFO_PRODUCTNAME}"
  WriteRegStr ${ROOT} "SOFTWARE\Clients\Mail\${INFO_PRODUCTNAME}\Capabilities" "ApplicationName" "${INFO_PRODUCTNAME}"
  WriteRegStr ${ROOT} "SOFTWARE\Clients\Mail\${INFO_PRODUCTNAME}\Capabilities" "ApplicationDescription" "An Open Source Lightweight E-Mail Client"
  WriteRegStr ${ROOT} "SOFTWARE\Clients\Mail\${INFO_PRODUCTNAME}\Capabilities" "ApplicationIcon" "$INSTDIR\${PRODUCT_EXECUTABLE},0"
  WriteRegStr ${ROOT} "SOFTWARE\Clients\Mail\${INFO_PRODUCTNAME}\Capabilities\UrlAssociations" "mailto" "${INFO_PRODUCTNAME}Mailto"
  WriteRegStr ${ROOT} "SOFTWARE\Clients\Mail\${INFO_PRODUCTNAME}\shell\open\command" "" "$\"$INSTDIR\${PRODUCT_EXECUTABLE}$\" $\"%1$\""

  WriteRegStr ${ROOT} "SOFTWARE\Classes\${INFO_PRODUCTNAME}Mailto" "" "URL:MailTo Protocol"
  WriteRegStr ${ROOT} "SOFTWARE\Classes\${INFO_PRODUCTNAME}Mailto" "URL Protocol" ""
  WriteRegStr ${ROOT} "SOFTWARE\Classes\${INFO_PRODUCTNAME}Mailto\DefaultIcon" "" "$INSTDIR\${PRODUCT_EXECUTABLE},0"
  WriteRegStr ${ROOT} "SOFTWARE\Classes\${INFO_PRODUCTNAME}Mailto\shell\open\command" "" "$\"$INSTDIR\${PRODUCT_EXECUTABLE}$\" $\"%1$\""

  WriteRegStr ${ROOT} "SOFTWARE\RegisteredApplications" "${INFO_PRODUCTNAME}" "SOFTWARE\Clients\Mail\${INFO_PRODUCTNAME}\Capabilities"
!macroend

## Remove the mail-client registration from the given registry root.
!macro daak.deleteMailRegistration ROOT
  DeleteRegKey ${ROOT} "SOFTWARE\Clients\Mail\${INFO_PRODUCTNAME}"
  DeleteRegKey ${ROOT} "SOFTWARE\Classes\${INFO_PRODUCTNAME}Mailto"
  DeleteRegValue ${ROOT} "SOFTWARE\RegisteredApplications" "${INFO_PRODUCTNAME}"
!macroend

## Write the uninstaller and its registry entry under the given root.
## Same values as the wails macro, plus InstallLocation (used by
## Daak.DetectPreviousInstallMode to default upgrades to the previous
## install's mode and folder).
!macro daak.writeUninstaller ROOT
  WriteUninstaller "$INSTDIR\uninstall.exe"

  WriteRegStr ${ROOT} "${UNINST_KEY}" "Publisher" "${INFO_COMPANYNAME}"
  WriteRegStr ${ROOT} "${UNINST_KEY}" "DisplayName" "${INFO_PRODUCTNAME}"
  WriteRegStr ${ROOT} "${UNINST_KEY}" "DisplayVersion" "${INFO_PRODUCTVERSION}"
  WriteRegStr ${ROOT} "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${PRODUCT_EXECUTABLE}"
  WriteRegStr ${ROOT} "${UNINST_KEY}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
  WriteRegStr ${ROOT} "${UNINST_KEY}" "QuietUninstallString" "$\"$INSTDIR\uninstall.exe$\" /S"
  WriteRegStr ${ROOT} "${UNINST_KEY}" "InstallLocation" "$INSTDIR"

  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD ${ROOT} "${UNINST_KEY}" "EstimatedSize" "$0"
!macroend

## Remove the uninstaller and its registry entry from the given root.
!macro daak.deleteUninstaller ROOT
  Delete "$INSTDIR\uninstall.exe"
  DeleteRegKey ${ROOT} "${UNINST_KEY}"
!macroend

Var Daak.CleanupRoot ; "HKLM" or "HKCU": the scope being removed
Var Daak.CleanupFolder ; install folder of the scope being removed

## Resolve the install folder of the scope in $Daak.CleanupRoot.
## InstallLocation first (written since 0.7.11), then the folder of
## UninstallString (the only record pre-0.7.11 installs left), then the
## scope's default folder. Result in $Daak.CleanupFolder, never empty.
Function Daak.ResolveScopeFolder
  SetRegView 64
  StrCpy $Daak.CleanupFolder ""
  ${If} $Daak.CleanupRoot == "HKLM"
    ReadRegStr $5 HKLM "${UNINST_KEY}" "InstallLocation"
  ${Else}
    ReadRegStr $5 HKCU "${UNINST_KEY}" "InstallLocation"
  ${EndIf}
  ${If} $5 != ""
    StrCpy $Daak.CleanupFolder $5
    Return
  ${EndIf}
  ${If} $Daak.CleanupRoot == "HKLM"
    ReadRegStr $5 HKLM "${UNINST_KEY}" "UninstallString"
  ${Else}
    ReadRegStr $5 HKCU "${UNINST_KEY}" "UninstallString"
  ${EndIf}
  ${If} $5 != ""
    ; UninstallString is '"<folder>\uninstall.exe"'. Strip the quotes,
    ; then take the parent folder.
    StrCpy $6 $5 1 0
    ${If} $6 == '"'
      StrLen $7 $5
      IntOp $7 $7 - 2
      StrCpy $5 $5 $7 1
    ${EndIf}
    ${GetParent} $5 $6
    ${If} $6 != ""
      StrCpy $Daak.CleanupFolder $6
      Return
    ${EndIf}
  ${EndIf}
  ${If} $Daak.CleanupRoot == "HKLM"
    StrCpy $Daak.CleanupFolder "$PROGRAMFILES64\${INFO_PRODUCTNAME}"
  ${Else}
    StrCpy $Daak.CleanupFolder "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
  ${EndIf}
FunctionEnd

## Remove the whole install in the scope named by $Daak.CleanupRoot
## ("HKLM" or "HKCU"): its files, uninstall key, mail-client
## registration and Start Menu shortcut. The user data folder is never
## touched. The uninstall key is deleted last, so its absence means the
## removal finished. Restores the shell context of the install being run,
## so the shortcuts created later land in the right Start Menu.
Function Daak.RemoveScope
  Call Daak.ResolveScopeFolder
  DetailPrint "Removing the previous install in $Daak.CleanupFolder."
  RMDir /r "$Daak.CleanupFolder"

  ${If} $Daak.CleanupRoot == "HKLM"
    !insertmacro daak.deleteMailRegistration HKLM
    SetShellVarContext all
  ${Else}
    !insertmacro daak.deleteMailRegistration HKCU
    SetShellVarContext current
  ${EndIf}
  Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"

  ${If} $MultiUser.InstallMode == "AllUsers"
    SetShellVarContext all
  ${Else}
    SetShellVarContext current
  ${EndIf}

  ${If} $Daak.CleanupRoot == "HKLM"
    DeleteRegKey HKLM "${UNINST_KEY}"
  ${Else}
    DeleteRegKey HKCU "${UNINST_KEY}"
  ${EndIf}
FunctionEnd

## Remove a previous install in the other scope than the one being
## installed, so an upgrade can never leave two Daak installs behind.
## Installing for all users runs elevated, so the per-user install is
## removed directly and silently. Installing just for you runs
## unelevated and cannot delete Program Files or HKLM keys, so the user
## is asked once: Yes relaunches this installer elevated as a removal
## helper (flag /RemoveAllUsers, installs nothing) and waits for it;
## No leaves both installs in place, informed.
Function Daak.RemoveOtherScope
  SetRegView 64
  ${If} $MultiUser.InstallMode == "AllUsers"
    ReadRegStr $5 HKCU "${UNINST_KEY}" "InstallLocation"
    ${If} $5 == ""
      ReadRegStr $5 HKCU "${UNINST_KEY}" "UninstallString"
    ${EndIf}
    ${If} $5 == ""
      Return
    ${EndIf}
    StrCpy $Daak.CleanupRoot "HKCU"
    Call Daak.RemoveScope
    Return
  ${EndIf}

  ; Installing just for you.
  ReadRegStr $5 HKLM "${UNINST_KEY}" "InstallLocation"
  ${If} $5 == ""
    ReadRegStr $5 HKLM "${UNINST_KEY}" "UninstallString"
  ${EndIf}
  ${If} $5 == ""
    Return
  ${EndIf}
  StrCpy $Daak.CleanupRoot "HKLM"
  Call Daak.ResolveScopeFolder
  ${If} ${Silent}
    DetailPrint "An install for all users was found at $Daak.CleanupFolder; leaving it in place (silent install)."
    Return
  ${EndIf}
  MessageBox MB_YESNO|MB_ICONQUESTION "An install for all users was found at $Daak.CleanupFolder.$\r$\n$\r$\nInstalling just for you will leave it in place, and you would have two Daak installs. Remove the all-users install now? (Needs administrator rights once.)" IDYES Daak.RemoveOtherScope.yes IDNO Daak.RemoveOtherScope.no
  Daak.RemoveOtherScope.no:
    DetailPrint "Keeping the install for all users at $Daak.CleanupFolder."
    Return
  Daak.RemoveOtherScope.yes:
    ExecShell "runas" '"$EXEPATH"' '/ELEVATED /RemoveAllUsers'
    ; Wait up to 60 seconds for the helper to finish. The helper deletes
    ; the uninstall key last, so the key disappearing means it is done.
    StrCpy $1 0
    Daak.RemoveOtherScope.waitLoop:
      Sleep 1000
      ReadRegStr $5 HKLM "${UNINST_KEY}" "UninstallString"
      ${If} $5 == ""
        DetailPrint "Removed the install for all users."
        Return
      ${EndIf}
      IntOp $1 $1 + 1
      ${If} $1 < 60
        Goto Daak.RemoveOtherScope.waitLoop
      ${EndIf}
    MessageBox MB_YESNO|MB_ICONEXCLAMATION "The install for all users could not be removed (the administrator prompt may have been cancelled). Continue installing just for you anyway, leaving both installs?" IDYES Daak.RemoveOtherScope.keep IDNO Daak.RemoveOtherScope.abort
    Daak.RemoveOtherScope.keep:
      DetailPrint "Keeping the install for all users at $Daak.CleanupFolder."
      Return
    Daak.RemoveOtherScope.abort:
      Abort
FunctionEnd

## Is Daak currently running? Pushes 1 if ${PRODUCT_EXECUTABLE} is running,
## 0 otherwise. The nsExec pop order is not relied on: both stack values are
## searched for the exe name (the return code is a number and never matches).
Function Daak.IsRunning
  nsExec::ExecToStack 'tasklist /FI "IMAGENAME eq ${PRODUCT_EXECUTABLE}" /NH'
  Pop $0
  Pop $1
  ${StrStr} $2 $0 "${PRODUCT_EXECUTABLE}"
  ${If} $2 == ""
    ${StrStr} $2 $1 "${PRODUCT_EXECUTABLE}"
  ${EndIf}
  ${If} $2 != ""
    Push 1
  ${Else}
    Push 0
  ${EndIf}
FunctionEnd

## Does a Daak 0.7.11+ hold the named quit event? Pushes 1 if the event
## exists — a running recent Daak holds it from startup until the process
## itself exits — 0 otherwise. This is the reliable "is Daak running"
## signal: the tasklist check below proved fragile (a missed match skips
## the handshake entirely and the file write then fails with "Error
## opening file for writing"). The event is session-local (Local\), so an
## elevated installer in the same logon session can open it.
Function Daak.QuitEventExists
  System::Call 'kernel32::OpenEventW(i 0x0002, i 0, w "${DAAK_QUIT_EVENT}") i .r0'
  ${If} $0 == 0
    Push 0
    Return
  ${EndIf}
  System::Call 'kernel32::CloseHandle(i r0)'
  Push 1
FunctionEnd

## Pushes 1 while Daak is still running, 0 once it is gone. A running
## Daak is gone only when BOTH checks agree: the quit event is absent
## (recent builds hold it until the process exits) AND tasklist no longer
## finds the process (older builds without the event). Either check alone
## can lie — tasklist parsing missed a running Daak in the field, and a
## 0.7.16-or-earlier build drops its event handle the moment it is
## signalled, before it has finished shutting down — so neither alone is
## trusted to say "gone".
Function Daak.IsRunningNow
  Call Daak.QuitEventExists
  Pop $0
  ${If} $0 == 1
    Push 1
    Return
  ${EndIf}
  Call Daak.IsRunning
  Pop $0
  Push $0
FunctionEnd

## Ask a running Daak to quit via the named quit event (Daak 0.7.11+).
## Pushes 1 if the event was signalled, 0 if there is no event — either the
## app is not running, or it is an older Daak without the handshake.
Function Daak.SignalQuit
  System::Call 'kernel32::OpenEventW(i 0x0002, i 0, w "${DAAK_QUIT_EVENT}") i .r0'
  ${If} $0 == 0
    Push 0
    Return
  ${EndIf}
  System::Call 'kernel32::SetEvent(i r0) i .r1'
  System::Call 'kernel32::CloseHandle(i r0)'
  Push 1
FunctionEnd

## Close a running Daak before its files are replaced. Detection is
## event-first: if the quit event exists, a recent Daak is running — signal
## it and wait up to ${DAAK_QUIT_WAIT_SECONDS} seconds, polling both
## checks. Only when there is no event at all does it fall back to
## tasklist, which covers only pre-0.7.11 copies that have no event (those
## still get the manual Retry box). If it is still running after that,
## offers to force-quit it — only ever as the fallback the user explicitly
## chooses, never as the first step. The "Run in background" setting is
## never touched, so there is nothing to forget re-enabling. If even force
## quit fails, falls back to asking the user to quit Daak by hand with
## Retry/Cancel.
Function Daak.CloseRunning
  Call Daak.QuitEventExists
  Pop $0
  ${If} $0 == 1
    DetailPrint "Daak is running (quit event found), asking it to quit gracefully..."
    Call Daak.SignalQuit
    Pop $0
    ${If} $0 == 1
      ; Give the app a moment to shut down before the first check: a
      ; 0.7.16-or-earlier build drops its event handle the moment it is
      ; signalled, so the first polls lean on tasklist alone — and a
      ; missed tasklist match right after signalling would otherwise send
      ; the installer straight into the locked file. 2 s grace + 13 s of
      ; polling keeps the total wait at ${DAAK_QUIT_WAIT_SECONDS} seconds.
      Sleep 2000
      StrCpy $1 0
      Daak.CloseRunning.waitLoop:
        Call Daak.IsRunningNow
        Pop $0
        ${If} $0 == 0
          DetailPrint "Daak quit."
          Return
        ${EndIf}
        IntOp $1 $1 + 1
        ${If} $1 < 13
          Sleep 1000
          Goto Daak.CloseRunning.waitLoop
        ${EndIf}
      DetailPrint "Daak did not quit within ${DAAK_QUIT_WAIT_SECONDS} seconds."
    ${Else}
      ; The event vanished between the two calls: the app is already on
      ; its way out. Re-check with tasklist before deciding anything.
      DetailPrint "Quit event disappeared; re-checking with tasklist."
      Call Daak.IsRunning
      Pop $0
      ${If} $0 == 0
        DetailPrint "No running Daak found."
        Return
      ${EndIf}
      DetailPrint "Daak is running (older version, no quit event)."
    ${EndIf}
  ${Else}
    Call Daak.IsRunning
    Pop $0
    ${If} $0 == 0
      DetailPrint "No running Daak found."
      Return
    ${EndIf}
    DetailPrint "Daak is running (older version, no quit event)."
  ${EndIf}

  ; Still running after the graceful wait. Offer force quit: a force kill
  ; can interrupt an in-flight sync, but the database is transactional
  ; (WAL) so it recovers on next start — and the user chose it explicitly.
  ; Yes force-quits and continues; No falls through to the manual
  ; Retry/Cancel box below, unchanged.
  MessageBox MB_YESNOCANCEL|MB_ICONEXCLAMATION "Daak is still running. Force quit Daak and continue with the installation?" IDYES Daak.CloseRunning.forceQuit IDNO Daak.CloseRunning.retryLoop
  Abort
  Daak.CloseRunning.forceQuit:
    nsExec::ExecToStack 'taskkill /F /IM ${PRODUCT_EXECUTABLE}'
    Pop $0
    Pop $1
    StrCpy $1 0
    Daak.CloseRunning.killWaitLoop:
      Sleep 1000
      Call Daak.IsRunningNow
      Pop $0
      ${If} $0 == 0
        DetailPrint "Daak force-quit."
        Return
      ${EndIf}
      IntOp $1 $1 + 1
      ${If} $1 < 5
        Goto Daak.CloseRunning.killWaitLoop
      ${EndIf}
    ; Even force quit did not stop it: fall back to the manual path.
    DetailPrint "Force quit did not stop Daak."
    Goto Daak.CloseRunning.retryLoop

  Daak.CloseRunning.retryLoop:
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Daak is still running, so its files cannot be replaced. To quit Daak, turn off $\"Run in background$\" in Settings and close the window, then choose Retry to continue." IDRETRY Daak.CloseRunning.retry
    Abort
  Daak.CloseRunning.retry:
    Call Daak.IsRunningNow
    Pop $0
    ${If} $0 == 0
      Return
    ${EndIf}
    Goto Daak.CloseRunning.retryLoop
FunctionEnd

## Default the install mode (and folder) from a previous install, so an
## upgrade replaces the old install instead of creating a second one in the
## other scope. An all-users install leaves its uninstall key under HKLM, a
## per-user install under HKCU. Installs before 0.7.11 wrote no
## InstallLocation, so fall back to the key's presence via UninstallString.
Function Daak.DetectPreviousInstallMode
  ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
  ${If} $0 != ""
    ${If} $MultiUser.Privileges == "Admin"
    ${OrIf} $MultiUser.Privileges == "Power"
      Call MultiUser.InstallMode.AllUsers
      StrCpy $INSTDIR $0
    ${EndIf}
    Return
  ${EndIf}
  ReadRegStr $0 HKCU "${UNINST_KEY}" "InstallLocation"
  ${If} $0 != ""
    Call MultiUser.InstallMode.CurrentUser
    StrCpy $INSTDIR $0
    Return
  ${EndIf}
  ReadRegStr $0 HKLM "${UNINST_KEY}" "UninstallString"
  ${If} $0 != ""
    ${If} $MultiUser.Privileges == "Admin"
    ${OrIf} $MultiUser.Privileges == "Power"
      Call MultiUser.InstallMode.AllUsers
    ${EndIf}
  ${EndIf}
FunctionEnd

## If the install mode was fixed to all-users without the install-mode page
## (a silent install, or /AllUsers on the command line) and this process is
## not carrying /ELEVATED yet, relaunch elevated and quit. The elevated copy
## carries /ELEVATED so it does not loop. Interactive installs are left
## alone here — the install-mode page handles their elevation.
Function Daak.MaybeRelaunchElevated
  ${If} $MultiUser.InstallMode != "AllUsers"
    Return
  ${EndIf}
  ${GetParameters} $0
  ${StrStr} $1 $0 "/ELEVATED"
  ${If} $1 != ""
    Return
  ${EndIf}
  ${StrStr} $1 $0 "/AllUsers"
  ${If} $1 == ""
  ${AndIfNot} ${Silent}
    Return
  ${EndIf}
  ExecShell "runas" '"$EXEPATH"' '/ELEVATED $0'
  Quit
FunctionEnd

Var Daak.ModePage
Var Daak.ModePage.AllUsers
Var Daak.ModePage.CurrentUser

## Install-mode page: per-user (no admin rights) or all-users (admin rights).
## Skipped for silent installs, when the mode was fixed on the command line,
## and for accounts that cannot install for all users anyway.
Function Daak.InstallModePre
  ${If} ${Silent}
    Abort
  ${EndIf}
  ${GetParameters} $0
  ${StrStr} $1 $0 "/AllUsers"
  ${If} $1 != ""
    Abort
  ${EndIf}
  ${StrStr} $1 $0 "/CurrentUser"
  ${If} $1 != ""
    Abort
  ${EndIf}
  ${If} $MultiUser.Privileges != "Admin"
  ${AndIf} $MultiUser.Privileges != "Power"
    Abort
  ${EndIf}

  !insertmacro MUI_HEADER_TEXT_PAGE "Install mode" "Choose who Daak is installed for"

  nsDialogs::Create 1018
  Pop $Daak.ModePage
  ${If} $Daak.ModePage == error
    Abort
  ${EndIf}

  ${NSD_CreateLabel} 0 0 100% 24u "Choose whether to install Daak just for you or for everyone who uses this computer. Installing for everyone requires administrator rights."
  Pop $0

  ${NSD_CreateRadioButton} 0 30u 100% 12u "Install for all users (requires administrator rights)"
  Pop $Daak.ModePage.AllUsers
  ${NSD_CreateRadioButton} 0 46u 100% 12u "Install just for me"
  Pop $Daak.ModePage.CurrentUser

  ${If} $MultiUser.InstallMode == "AllUsers"
    SendMessage $Daak.ModePage.AllUsers ${BM_SETCHECK} ${BST_CHECKED} 0
  ${Else}
    SendMessage $Daak.ModePage.CurrentUser ${BM_SETCHECK} ${BST_CHECKED} 0
  ${EndIf}

  nsDialogs::Show
FunctionEnd

Function Daak.InstallModeLeave
  SendMessage $Daak.ModePage.AllUsers ${BM_GETCHECK} 0 0 $0
  ${If} $0 == ${BST_CHECKED}
    Call MultiUser.InstallMode.AllUsers
    ; Installing for all users needs elevation: relaunch elevated and let
    ; the elevated copy carry on. /ELEVATED keeps it from looping.
    ${GetParameters} $1
    ExecShell "runas" '"$EXEPATH"' '/ELEVATED /AllUsers $1'
    Quit
  ${Else}
    Call MultiUser.InstallMode.CurrentUser
  ${EndIf}
FunctionEnd

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
PageEx custom
  PageCallbacks Daak.InstallModePre Daak.InstallModeLeave
  Caption " "
PageExEnd
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uinstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
InstallDir "$PROGRAMFILES64\${INFO_PRODUCTNAME}" # Default installing folder ($PROGRAMFILES is Program Files folder).
ShowInstDetails show # This will always show the installation details.

Function .onInit
   SetRegView 64

   !insertmacro MULTIUSER_INIT

   ; Removal helper: an unelevated installer that is installing just for
   ; you over an all-users install relaunches this installer elevated
   ; with /RemoveAllUsers. The helper removes ONLY the all-users
   ; install, then quits. It never installs anything.
   ${GetParameters} $0
   ${StrStr} $1 $0 "/RemoveAllUsers"
   ${If} $1 != ""
     ${If} $MultiUser.Privileges != "Admin"
     ${AndIf} $MultiUser.Privileges != "Power"
       ExecShell "runas" '"$EXEPATH"' '/ELEVATED /RemoveAllUsers'
       Quit
     ${EndIf}
     Call Daak.CloseRunning
     StrCpy $Daak.CleanupRoot "HKLM"
     Call Daak.RemoveScope
     Quit
   ${EndIf}

   Call Daak.DetectPreviousInstallMode

   Call Daak.MaybeRelaunchElevated

   Call Daak.CloseRunning

   !insertmacro wails.checkArchitecture
FunctionEnd

Section
    ; Remove a previous install in the other scope first, so an upgrade
    ; can never leave two Daak installs behind.
    Call Daak.RemoveOtherScope

    !insertmacro wails.webview2runtime

    SetOutPath $INSTDIR

    !insertmacro wails.files

    ; Ship the app icon next to the exe: the notifier points the toast
    ; IconUri at $INSTDIR\icon.ico (internal/notification/toast_identity.go).
    ; Same relative path as MUI_ICON above, so it exists at build time.
    File "..\icon.ico"

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols

    ; Register as a Windows Mail client so Daak appears in
    ; Settings → Default Apps → Email (alongside Outlook/Thunderbird).
    ; Per-user installs register under HKCU, all-users installs under HKLM.
    SetRegView 64
    ${If} $MultiUser.InstallMode == "AllUsers"
      !insertmacro daak.writeMailRegistration HKLM
      !insertmacro daak.writeUninstaller HKLM
    ${Else}
      !insertmacro daak.writeMailRegistration HKCU
      !insertmacro daak.writeUninstaller HKCU
    ${EndIf}
SectionEnd

## Uninstaller variants of the quit handshake. Same behaviour as the
## installer side; the message names the uninstall instead of the install.
Function un.Daak.IsRunning
  nsExec::ExecToStack 'tasklist /FI "IMAGENAME eq ${PRODUCT_EXECUTABLE}" /NH'
  Pop $0
  Pop $1
  ${UnStrStr} $2 $0 "${PRODUCT_EXECUTABLE}"
  ${If} $2 == ""
    ${UnStrStr} $2 $1 "${PRODUCT_EXECUTABLE}"
  ${EndIf}
  ${If} $2 != ""
    Push 1
  ${Else}
    Push 0
  ${EndIf}
FunctionEnd

## Does a Daak 0.7.11+ hold the named quit event? Pushes 1 if the event
## exists — a running recent Daak holds it from startup until the process
## itself exits — 0 otherwise. This is the reliable "is Daak running"
## signal: the tasklist check below proved fragile (a missed match skips
## the handshake entirely and the file write then fails with "Error
## opening file for writing"). The event is session-local (Local\), so an
## elevated uninstaller in the same logon session can open it.
Function un.Daak.QuitEventExists
  System::Call 'kernel32::OpenEventW(i 0x0002, i 0, w "${DAAK_QUIT_EVENT}") i .r0'
  ${If} $0 == 0
    Push 0
    Return
  ${EndIf}
  System::Call 'kernel32::CloseHandle(i r0)'
  Push 1
FunctionEnd

## Pushes 1 while Daak is still running, 0 once it is gone. A running
## Daak is gone only when BOTH checks agree: the quit event is absent
## (recent builds hold it until the process exits) AND tasklist no longer
## finds the process (older builds without the event). Either check alone
## can lie — tasklist parsing missed a running Daak in the field, and a
## 0.7.16-or-earlier build drops its event handle the moment it is
## signalled, before it has finished shutting down — so neither alone is
## trusted to say "gone".
Function un.Daak.IsRunningNow
  Call un.Daak.QuitEventExists
  Pop $0
  ${If} $0 == 1
    Push 1
    Return
  ${EndIf}
  Call un.Daak.IsRunning
  Pop $0
  Push $0
FunctionEnd

Function un.Daak.SignalQuit
  System::Call 'kernel32::OpenEventW(i 0x0002, i 0, w "${DAAK_QUIT_EVENT}") i .r0'
  ${If} $0 == 0
    Push 0
    Return
  ${EndIf}
  System::Call 'kernel32::SetEvent(i r0) i .r1'
  System::Call 'kernel32::CloseHandle(i r0)'
  Push 1
FunctionEnd

Function un.Daak.CloseRunning
  Call un.Daak.QuitEventExists
  Pop $0
  ${If} $0 == 1
    DetailPrint "Daak is running (quit event found), asking it to quit gracefully..."
    Call un.Daak.SignalQuit
    Pop $0
    ${If} $0 == 1
      ; Give the app a moment to shut down before the first check: a
      ; 0.7.16-or-earlier build drops its event handle the moment it is
      ; signalled, so the first polls lean on tasklist alone — and a
      ; missed tasklist match right after signalling would otherwise send
      ; the uninstaller straight into the locked file. 2 s grace + 13 s
      ; of polling keeps the total wait at ${DAAK_QUIT_WAIT_SECONDS}
      ; seconds.
      Sleep 2000
      StrCpy $1 0
      un.Daak.CloseRunning.waitLoop:
        Call un.Daak.IsRunningNow
        Pop $0
        ${If} $0 == 0
          DetailPrint "Daak quit."
          Return
        ${EndIf}
        IntOp $1 $1 + 1
        ${If} $1 < 13
          Sleep 1000
          Goto un.Daak.CloseRunning.waitLoop
        ${EndIf}
      DetailPrint "Daak did not quit within ${DAAK_QUIT_WAIT_SECONDS} seconds."
    ${Else}
      ; The event vanished between the two calls: the app is already on
      ; its way out. Re-check with tasklist before deciding anything.
      DetailPrint "Quit event disappeared; re-checking with tasklist."
      Call un.Daak.IsRunning
      Pop $0
      ${If} $0 == 0
        DetailPrint "No running Daak found."
        Return
      ${EndIf}
      DetailPrint "Daak is running (older version, no quit event)."
    ${EndIf}
  ${Else}
    Call un.Daak.IsRunning
    Pop $0
    ${If} $0 == 0
      DetailPrint "No running Daak found."
      Return
    ${EndIf}
    DetailPrint "Daak is running (older version, no quit event)."
  ${EndIf}

  ; Still running after the graceful wait. Offer force quit: a force kill
  ; can interrupt an in-flight sync, but the database is transactional
  ; (WAL) so it recovers on next start — and the user chose it explicitly.
  ; Yes force-quits and continues; No falls through to the manual
  ; Retry/Cancel box below, unchanged.
  MessageBox MB_YESNOCANCEL|MB_ICONEXCLAMATION "Daak is still running. Force quit Daak and continue with the uninstall?" IDYES un.Daak.CloseRunning.forceQuit IDNO un.Daak.CloseRunning.retryLoop
  Abort
  un.Daak.CloseRunning.forceQuit:
    nsExec::ExecToStack 'taskkill /F /IM ${PRODUCT_EXECUTABLE}'
    Pop $0
    Pop $1
    StrCpy $1 0
    un.Daak.CloseRunning.killWaitLoop:
      Sleep 1000
      Call un.Daak.IsRunningNow
      Pop $0
      ${If} $0 == 0
        DetailPrint "Daak force-quit."
        Return
      ${EndIf}
      IntOp $1 $1 + 1
      ${If} $1 < 5
        Goto un.Daak.CloseRunning.killWaitLoop
      ${EndIf}
    ; Even force quit did not stop it: fall back to the manual path.
    DetailPrint "Force quit did not stop Daak."
    Goto un.Daak.CloseRunning.retryLoop

  un.Daak.CloseRunning.retryLoop:
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Daak is still running, so it cannot be uninstalled yet. To quit Daak, turn off $\"Run in background$\" in Settings and close the window, then choose Retry to continue." IDRETRY un.Daak.CloseRunning.retry
    Abort
  un.Daak.CloseRunning.retry:
    Call un.Daak.IsRunningNow
    Pop $0
    ${If} $0 == 0
      Return
    ${EndIf}
    Goto un.Daak.CloseRunning.retryLoop
FunctionEnd

## Detect the previous install's mode for the uninstaller, same as the
## installer side. Sets $MultiUser.InstallMode (and the shell context).
Function un.Daak.DetectPreviousInstallMode
  ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
  ${If} $0 != ""
    ${If} $MultiUser.Privileges == "Admin"
    ${OrIf} $MultiUser.Privileges == "Power"
      Call un.MultiUser.InstallMode.AllUsers
      StrCpy $INSTDIR $0
    ${EndIf}
    Return
  ${EndIf}
  ReadRegStr $0 HKCU "${UNINST_KEY}" "InstallLocation"
  ${If} $0 != ""
    Call un.MultiUser.InstallMode.CurrentUser
    StrCpy $INSTDIR $0
    Return
  ${EndIf}
  ReadRegStr $0 HKLM "${UNINST_KEY}" "UninstallString"
  ${If} $0 != ""
    ${If} $MultiUser.Privileges == "Admin"
    ${OrIf} $MultiUser.Privileges == "Power"
      Call un.MultiUser.InstallMode.AllUsers
    ${EndIf}
  ${EndIf}
FunctionEnd

Function un.onInit
  SetRegView 64

  !insertmacro MULTIUSER_UNINIT

  Call un.Daak.DetectPreviousInstallMode

  ; An all-users uninstall needs elevation to remove files from Program
  ; Files and keys under HKLM. Relaunch elevated unless this copy already
  ; carries /ELEVATED.
  ${If} $MultiUser.InstallMode == "AllUsers"
    ${un.GetParameters} $0
    ${UnStrStr} $1 $0 "/ELEVATED"
    ${If} $1 == ""
      ExecShell "runas" '"$EXEPATH"' '/ELEVATED $0'
      Quit
    ${EndIf}
  ${EndIf}

  Call un.Daak.CloseRunning
FunctionEnd

Section "uninstall"
    RMDir /r "$AppData\${PRODUCT_EXECUTABLE}" # Remove the WebView2 DataPath

    RMDir /r $INSTDIR

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    ; Clean up the mail-client registration written during install.
    SetRegView 64
    ${If} $MultiUser.InstallMode == "AllUsers"
      !insertmacro daak.deleteMailRegistration HKLM
      !insertmacro daak.deleteUninstaller HKLM
    ${Else}
      !insertmacro daak.deleteMailRegistration HKCU
      !insertmacro daak.deleteUninstaller HKCU
    ${EndIf}
SectionEnd
