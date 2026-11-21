Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows you to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
## 
## For development first build bin/steam-achievement-tracker.exe (wails3 task windows:build). To build the installer
## from that exe without rebuilding, run: wails3 task windows:create:nsis:installer (see build/windows/Taskfile.yml).
## Or call makensis on this file with the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
####
## The following information is taken from the wails_tools.nsh file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "my-project" # Default "steam-achievement-tracker"
## !define INFO_COMPANYNAME    "My Company" # Default "gjrud"
## !define INFO_PRODUCTNAME    "My Product Name" # Default "Steam Achievement Tracker"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "0.1.0"
## !define INFO_COPYRIGHT      "(c) Now, My Company" # Default "© now, My Company"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
## !define WAILS_INSTALL_SCOPE     "user"             # Default "machine" - set to "user" for per-user install ($LOCALAPPDATA) without UAC prompt
####
## Include the wails tools
####
# Skip the stock wails.webview2runtime macro. The app.webview2runtime macro below replaces it, because
# wails_tools.nsh is generated and must not be edited.
!define WAILS_SKIP_WEBVIEW2
!include "wails_tools.nsh"

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
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
# The install folder page is only for machine scope. The user-scope installer always installs to InstallDir below
# (%LOCALAPPDATA%\Programs\<product>), so it has no folder choice.
!if "${WAILS_INSTALL_SCOPE}" != "user"
    !insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!endif
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_INSTFILES # Uninstalling page

!insertmacro MUI_LANGUAGE "English" # Set the Language of the installer

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
!if "${WAILS_INSTALL_SCOPE}" == "user"
    InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
!else
    InstallDir "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
!endif
ShowInstDetails show # This will always show the installation details.

Function .onInit
   !insertmacro wails.checkArchitecture
FunctionEnd

!define WV2_CLIENT_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!define WV2_HKLM_KEY "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WV2_CLIENT_GUID}"
!define WV2_HKCU_KEY "Software\Microsoft\EdgeUpdate\Clients\${WV2_CLIENT_GUID}"

# Install the WebView2 Runtime with the bootstrapper, then confirm it is registered. Detection matches the stock
# wails.webview2runtime macro. The bootstrapper stub can exit before the Edge update service (SYSTEM) finishes
# installing the runtime, so the exit code alone is not trusted: poll the registry for up to 120 s.
# Uses $0-$3. Nothing later in the Section reads them before they are rewritten.
!macro app.webview2runtime
    SetRegView 64
    ReadRegStr $0 HKLM "${WV2_HKLM_KEY}" "pv"
    ${If} $0 != ""
        Goto wv2_done
    ${EndIf}

    ${If} ${REQUEST_EXECUTION_LEVEL} == "user"
        ReadRegStr $0 HKCU "${WV2_HKCU_KEY}" "pv"
        ${If} $0 != ""
            Goto wv2_done
        ${EndIf}
    ${EndIf}

    SetDetailsPrint both
    DetailPrint "Installing: WebView2 Runtime"
    SetDetailsPrint listonly

    InitPluginsDir
    CreateDirectory "$pluginsdir\webview2bootstrapper"
    SetOutPath "$pluginsdir\webview2bootstrapper"
    File "MicrosoftEdgeWebview2Setup.exe"
    # ExecWait leaves $1 untouched and only sets the error flag when the exe cannot start; the earlier registry
    # misses also set that flag, so clear it first.
    ClearErrors
    ExecWait '"$pluginsdir\webview2bootstrapper\MicrosoftEdgeWebview2Setup.exe" /silent /install' $1
    ${If} ${Errors}
        StrCpy $1 "error"
        Goto wv2_warn
    ${EndIf}

    DetailPrint "Waiting for WebView2 Runtime registration..."
    StrCpy $2 0
    wv2_wait:
        ReadRegStr $0 HKLM "${WV2_HKLM_KEY}" "pv"
        ${If} $0 != ""
            Goto wv2_done
        ${EndIf}
        ${If} ${REQUEST_EXECUTION_LEVEL} == "user"
            ReadRegStr $0 HKCU "${WV2_HKCU_KEY}" "pv"
            ${If} $0 != ""
                Goto wv2_done
            ${EndIf}
        ${EndIf}
        ${If} $2 < 120
            IntOp $2 $2 + 1
            Sleep 1000
            Goto wv2_wait
        ${EndIf}

    wv2_warn:
        ${If} $1 == "error"
            StrCpy $3 "error"
        ${Else}
            IntFmt $3 "0x%08X" $1
        ${EndIf}
        DetailPrint "WebView2 Runtime not detected after setup (code $3)"
        MessageBox MB_ICONEXCLAMATION|MB_OK "The Microsoft WebView2 Runtime was not detected after setup (code $3). It may still be installing in the background.$\r$\n$\r$\nThe app needs it to run. If the app does not start, install it from https://go.microsoft.com/fwlink/p/?LinkId=2124703 and run this installer again." /SD IDOK

    wv2_done:
    SetDetailsPrint both
!macroend

Section
    !insertmacro wails.setShellContext

    !insertmacro app.webview2runtime

    SetOutPath $INSTDIR
    
    !insertmacro wails.files

    # Start Menu entry only. No Desktop shortcut (Linux installs no Desktop icon either).
    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols
    
    !insertmacro wails.writeUninstaller
SectionEnd

Section "uninstall" 
    !insertmacro wails.setShellContext

    # Remove only the files this installer writes to $INSTDIR: the app exe (wails.files) and uninstall.exe
    # (wails.writeUninstaller). Machine-scope installs can pick any folder on the directory page, so never RMDir /r
    # $INSTDIR; that would wipe whatever else lives there (for example %LocalAppData% or the app data folder).
    Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"
    !insertmacro wails.deleteUninstaller

    # In-app updates leave the replaced exe as <exe>.old.<n> (and sometimes <exe>.bak) next to the app exe. Delete
    # them by name pattern, not recursively, so an unrelated file in $INSTDIR is never touched.
    Delete "$INSTDIR\${PRODUCT_EXECUTABLE}.old.*"
    Delete "$INSTDIR\${PRODUCT_EXECUTABLE}.bak"

    # Non-recursive: removes $INSTDIR only if it is now empty. Runs after uninstall.exe is deleted.
    RMDir "$INSTDIR"

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    # User data is NOT removed, by design. Data and logs (library, settings, log file) live in
    # %LocalAppData%\steam-achievement-tracker. Covers, the WebView2 profile and other cache live in
    # %LocalAppData%\cache\steam-achievement-tracker (internal/appdata). Both are kept, so reinstalling restores
    # them. Do not add RMDir on these paths here. The Steam Web API key is in Windows Credential Manager and is not
    # removed either.
    #
    # The WebView2 profile moved from Wails' default %AppData%\<exe name> to the cache directory (main.go). Installs
    # made before that move leave an orphaned %AppData%\<exe name> folder (browser state only, no user data). It is
    # not removed here: the folder name is the exe name of the version that created it, and releases rename the exe
    # per version, so this uninstaller cannot reliably find it.
SectionEnd
