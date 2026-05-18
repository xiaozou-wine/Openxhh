#define MyAppName "Openxhh"

#define MyAppVersion GetEnv("OPENXHH_VERSION") == "" ? "dev" : GetEnv("OPENXHH_VERSION")

#define MyAppPublisher "Openxhh"

#define MyAppURL "https://github.com/Www8881313/Openxhh"

#define MyAppExeName "Openxhh.exe"

#define MyAppWebUIExeName "Openxhh-webui.exe"



[Setup]

AppId={{B679B38F-7139-4D5F-B8B5-80C6A1F5576E}

AppName={#MyAppName}

AppVersion={#MyAppVersion}

AppPublisher={#MyAppPublisher}

AppPublisherURL={#MyAppURL}

AppSupportURL={#MyAppURL}

AppUpdatesURL={#MyAppURL}

DefaultDirName={autopf}\Openxhh

DefaultGroupName=Openxhh

DisableProgramGroupPage=yes

OutputDir=..\..\dist\installer

OutputBaseFilename=Openxhh-Setup-x64

Compression=lzma2

SolidCompression=yes

WizardStyle=modern

ArchitecturesAllowed=x64compatible

ArchitecturesInstallIn64BitMode=x64compatible

PrivilegesRequired=admin

UninstallDisplayIcon={app}\{#MyAppWebUIExeName}



[Languages]

Name: "chinesesimp"; MessagesFile: "compiler:Default.isl"



[Tasks]

Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务："; Flags: checkedonce



[Dirs]

Name: "{commonappdata}\Openxhh"; Permissions: users-modify

Name: "{commonappdata}\Openxhh\log"; Permissions: users-modify



[Files]

Source: "..\..\dist\windows\{#MyAppExeName}"; DestDir: "{app}"; Flags: ignoreversion

Source: "..\..\dist\windows\{#MyAppWebUIExeName}"; DestDir: "{app}"; Flags: ignoreversion



[Icons]

Name: "{autoprograms}\Openxhh 控制台"; Filename: "{app}\{#MyAppWebUIExeName}"; Parameters: "-root ""{commonappdata}\Openxhh"" -bin ""{app}\{#MyAppExeName}"""; WorkingDir: "{commonappdata}\Openxhh"

Name: "{autodesktop}\Openxhh 控制台"; Filename: "{app}\{#MyAppWebUIExeName}"; Parameters: "-root ""{commonappdata}\Openxhh"" -bin ""{app}\{#MyAppExeName}"""; WorkingDir: "{commonappdata}\Openxhh"; Tasks: desktopicon



[Run]

Filename: "{app}\{#MyAppWebUIExeName}"; Parameters: "-root ""{commonappdata}\Openxhh"" -bin ""{app}\{#MyAppExeName}"""; WorkingDir: "{commonappdata}\Openxhh"; Description: "启动 Openxhh 控制台"; Flags: nowait postinstall skipifsilent
