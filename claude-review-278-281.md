# Code review: PR #278, #279, #280, #281

Recenzent: Claude (Opus 5.5), 2026-10-01. Revize:

| PR | Název | Head |
|----|-------|------|
| #278 | `--backup` read-only + systemd `CAP_NET_BIND_SERVICE` | `5794653` |
| #279 | menu ukazuje sekce z rolí | `cc2fdf0` |
| #280 | deploy používá credentials z Registries | `ef8e7d3` |
| #281 | audit log ukazuje detail a host | `d440dc1` |

Review jsem dělal čtením diffů a okolního kódu. Testy jsem nespouštěl.

## Shrnutí

| PR | Verdikt | Nálezy |
|----|---------|--------|
| #278 | Opravit před merge (drobné) | 1× střední, 1× nízký, 1× info |
| #279 | **OK k merge** | jen nit |
| #280 | Opravit před merge | 1× střední, 2× nízký, 1× otevřená otázka |
| #281 | Opravit před merge | 1× střední |

Napříč všemi: každý z PR přidává řádek na **stejné místo** v `CHANGELOG.md`
(hned pod `### Fixed`). Druhý a každý další merge proto skončí konfliktem.
Ten je triviální, ale po každém merge je potřeba rebase.

---

## #278: `dockercmd --backup` read-only + systemd unit

### [STŘEDNÍ] Každá chyba při otevření se hlásí jako „chybí databáze“ nebo „cizí soubor“

`cmd/dockercmd/main.go` (`backupDataDir`) převádí **každou** chybu
z `OpenSnapshotSource`, která není `ErrNotADatabase`, na jednu hlášku:
„no database at … — point at the right one with --data-dir (a packaged install
uses /var/lib/dockercmd)“. Původní chyba se zahodí. `OpenSnapshotSource` zase
balí **každou** chybu dotazu na identity tabulky do `ErrNotADatabase`.

Co to způsobí:
- `os.Stat` vrátí `EACCES`: uživatel spustil backup bez `sudo` s
  `--data-dir /var/lib/dockercmd`. Dostane radu, ať použije `--data-dir`
  s `/var/lib/dockercmd`, tedy přesně to, co právě udělal.
- `docker-commander.db` je adresář: hláška „no database“. Test sice ověří, že
  backup selže, ale ne hlášku.
- Soubor existuje, ale SQLite ho neotevře nebo nepřečte (práva, read-only
  `-shm` od běžícího serveru jiného uživatele, `database is locked` po 5 s):
  hláška „**not a Docker Commander database**“. To je nepravdivé a nebezpečné:
  operátor by mohl začít „opravovat“ databázi, která je v pořádku.

Návrh: „no database“ hlásit jen pro `errors.Is(err, fs.ErrNotExist)`. Ostatní
chyby vracet obalené i s původní příčinou. V `OpenSnapshotSource` dávat
`ErrNotADatabase` jen tam, kde dotaz uspěl a tabulka chybí, nebo kde SQLite
vrátí `SQLITE_NOTADB` („file is not a database“). Přidat test na
nečitelný soubor (`chmod 000`, mimo root), který ověří hlášku.

### [NÍZKÝ] Doc komentář `flagValue` teď patří jiné funkci

V `main.go` je nová funkce `backupDataDir` vložená **mezi** komentář
`// flagValue returns the value following …` a `func flagValue`. Komentář teď
stojí nad komentářem `backupDataDir` (godoc je spojí do jednoho) a `flagValue`
zůstane bez dokumentace. Gofmt ani vet to nezachytí. Stačí přesunout ty dva
řádky zpátky nad `func flagValue`.

### [INFO] Ambientní capability dědí všechny potomky

`AmbientCapabilities=CAP_NET_BIND_SERVICE` dostane i každý proces, který server
spustí: `docker`, `docker compose` s pluginy, `ssh`. Je to malé riziko
(jen bind portu pod 1024) a s `NoNewPrivileges` jde o běžný vzor. Komentář
v unitu ale říká „nothing else is added“. Dodal bych, že capability dědí i
potomci, aby to nikoho nepřekvapilo. Že unit nemá `CapabilityBoundingSet=`,
které by ambientní cap tiše zrušilo, jsem ověřil. Bind 443 pod skutečným unitem
podle PR ověřený není.

### Co je v pořádku
- `mode=ro` + kontrola identity tabulek + odmítnutí ne-regulárního souboru
  opravuje všechny tři popsané scénáře. Testy jsou dobře mutované, `mode=ro`
  hlídá `TestSnapshotSourceRefusesWrites`.
- Live-WAL round-trip přes `backup.Restore` je přesně ten test, který tu chyběl.
- Drobnost: DSN `file:%s?mode=ro` se rozbije na cestě s `?` nebo `#`. Stejný
  vzor má ale i `store.Open`, takže to není regrese.

---

## #279: menu ukazuje sekce z rolí

**Bez nálezů. OK k merge.**

- `effectiveSections` teď čte `EffectiveGrants`, tedy stejný zdroj jako
  `checkAccess`. Při chybě vrátí prázdné menu (fail closed).
- `TestMenuMatchesWhatTheServerAllows` ověřuje shodu v obou směrech pro všechny
  sekce. To je správná invarianta a pokryje i budoucí rozjetí obou cest.
- Host scope: role omezená na host 7 ukáže sekci v menu i na lokálním daemonu.
  To je konzistentní s `Grant.HasHost`, kde je lokální daemon vždy v dosahu.
- Nit: `effectiveSections` načítá `DisabledSections` jednou sama (chybu
  ignoruje, to tu bylo už dřív) a podruhé uvnitř `EffectiveGrants`. Pro
  ne-adminy to nevadí, protože `EffectiveGrants` při chybě skončí fail closed.
  Jen jeden dotaz navíc na každé `/api/auth/me`.

---

## #280: deploy používá credentials z Registries

### [STŘEDNÍ] Jeden rozbitý záznam nebo config shodí všechny deploye

`AllRegistryAuths` záměrně vrací chybu, když nejde dešifrovat **jakýkoli**
řádek (nebo chybí cipher). `ComposeRegistryEnv` vrací chybu, když
`config.json` uživatele není validní JSON nebo nejde přečíst (jakákoli chyba
kromě `ENOENT`). Obojí se volá v `projectDeployEnv` a v `StackRedeploy` ještě
**před** compose, takže:

- jeden poškozený nebo starým klíčem zašifrovaný registry řádek, nebo
- poškozený `~/.docker/config.json`

zablokuje **všechny** project deploye, obnovy revizí, MCP deploye a lokální
stack redeploye. Týká se to i projektů, které používají jen veřejné image a
před tímto PR fungovaly. Samotné Docker CLI na poškozený `config.json` jen
vypíše `WARNING: Error loading config file` a pokračuje. Toto PR je tedy
přísnější než nástroj, který obaluje.

Argument v komentáři („tichá ztráta credentialu skončí nejasnou 401“) dává
smysl. Lepší kompromis podle mě: řádek, který nejde dešifrovat, vynechat a
napsat to do `note` nebo do výstupu deploye („registry X skipped: cannot
decrypt“). U nečitelného nebo poškozeného `config.json` buď vynechat jen merge
(vrátit noop env a varování), nebo začít z prázdného configu jako u
`src == ""`. `TestComposeRegistryEnvRefusesACorruptConfig` by se pak upravil
na „deploy pokračuje a v poznámce je varování“.

### [NÍZKÝ] Text poznámky a CHANGELOG odporuje tomu, co kód dělá

`sshRegistryNote` i CHANGELOG říkají „Credentials stored under Registries are
**not sent to other machines**“. Project deploy na vzdálený host (SSH nebo TCP)
je ale **posílá**: compose běží lokálně a předá je vzdálenému daemonu
v `X-Registry-Auth`. Popis PR to sám uvádí („on any target host, because
compose runs here and passes the credentials to the daemon“). Pravdivá věta
je užší: *tento* redeploy (CLI stack na SSH hostu) je na ten host nekopíruje.
Viz paměť „Docs must match code“. Návrh: „…this redeploy doesn't copy the
credentials stored under Registries to the host.“

### [NÍZKÝ] Více credentialů pro stejný registry: deploy a Images vyberou jiný

Tabulka `registries` nemá na `address` UNIQUE. `AllRegistryAuths` čte
`ORDER BY id` a do `authMap[key]` zapisuje postupně, takže vyhraje
**poslední** záznam. `AuthForHost` (pull a push na stránce Images) používá
`WHERE address = ? LIMIT 1` bez `ORDER BY`, což v SQLite fakticky vrátí
**první**. Dva tokeny pro `ghcr.io` (třeba pro dvě organizace) tak dají
v Images jiný výsledek než při deployi. Návrh: sjednotit výběr (první podle
id na obou místech), případně při vytvoření druhého záznamu pro stejnou
adresu zobrazit varování.

### [OTEVŘENÁ OTÁZKA] Kdo smí deployovat, ten teď dosáhne na všechna hesla registrů

Každý deploy dostane config se **všemi** uloženými credentials, včetně deployů
uživatele, který nemá sekci `registries`. Compose interpoluje proměnné
z prostředí procesu, takže compose soubor s `build: { context: ${DOCKER_CONFIG} }`
pošle `config.json` s hesly jako build context a `COPY` ho dostane do image.
Podobně by šlo dostat i data dir s šifrovacím klíčem
(`context: /var/lib/dockercmd`). Pro build context jsem v policy kódu
nenašel žádnou kontrolu (grep na `build.context` / `BuildContext`
v `internal/api/*policy*` nic nevrátil). **Neověřil jsem to ale end-to-end.**
Pokud to tak je, oprávnění k deployi už dnes znamená přístup k souborům
serveru a toto PR přidává jen další soubor. Je to ale dobrý důvod podívat
se, jestli policy hlídá `build.context` mimo složku projektu stejně jako
externí bind mounty.

### Co je v pořádku
- Přepsání helperu prázdným jménem (`credHelpers[host] = ""`) odpovídá
  `getConfiguredCredentialStore`. Integration test s neexistujícím
  `credsStore` to dokazuje na skutečném CLI. Výborný test.
- Klíč pro Docker Hub (`https://index.docker.io/v1/`) i obě varianty klíče
  v `credHelpers` jsou správně.
- Práva `0700` a `0600`, úklid po běhu, symlinky místo kopií (úklid nemaže
  cíle linků) a přeskočení `config.json.lock` jsou v pořádku.
- Pentest na `config.json` v pracovním adresáři projektu (`HOME=""`) je
  přesně ta správná otázka.
- `DOCKER_CONFIG` se přidává až za secrets projektu. Project secret se stejným
  jménem ho tedy nepřebije (Go `exec` si nechá poslední hodnotu).
- `DOCKER_CERT_PATH` pro TLS hosty je nastavený explicitně, takže změna
  `DOCKER_CONFIG` ho neovlivní.

---

## #281: audit log ukazuje detail a host

### [STŘEDNÍ] `hostId = 0` není „akce bez hostu“, ale hlavně „výchozí lokální daemon“

`Server.audit` bere `hostID` z `hostParam(r)`, což je `0`, když request nemá
`?host=`. Frontend (`web/src/lib/host.ts`, `hostParam()`) parametr **vynechá**,
když je vybraný výchozí lokální host (`hostId == null`). Backend (`resolveHostID`,
`NormalizeHostID`) chápe `0` jako „the default local host“.

Takže `container.stop` na lokálním daemonu se v novém sloupci Host ukáže
jako `—`, kterou PR i komentář v `hostLabel` popisují jako „action with no
host“. Je to právě ten případ, na který měla oprava odpovídat („on which
server was this container stopped“). Navíc ta samá lokální akce ukáže jméno
lokálního hostu, když uživatel lokální host vybral explicitně (pošle se
`?host=<id lokálního řádku>`). Ta samá věc se tedy zobrazí dvěma různými
způsoby.

Návrh (od nejmenšího zásahu):
1. Ve frontendu u `0` zobrazit „local“ (nebo jméno řádku `kind === "local"`
   ze seznamu hostů) u akcí, které na hostu běží, a `—` jen u akcí bez hostu.
   Rozlišit to jde jen podle prefixu akce (`container.*`, `image.*`, …), což
   je křehké.
2. Čistší je to v backendu: v `Server.audit` normalizovat (`0` → id lokálního
   řádku, případně uložit `-1`/NULL pro routy, které host vůbec nemají).
   To by ale znamenalo Go změnu v PR, které je teď čistě frontendové.

Minimálně je potřeba opravit komentáře, CHANGELOG a test
(`hostLabel(0) === "—"`), aby netvrdily „no host“.

### Co je v pořádku
- `#id` místo hádání jména pro host mimo scope je správně a nic neprozradí.
- Hosty i záznamy se načítají v `Promise.all` a `setHosts` proběhne před
  `setEntries`. To obchází memo v `useListControls` (deps
  `[items, query, status]`), takže uložený dotaz vidí jména hostů hned.
  Je to ale křehké: kdyby se hosty někdy načítaly zvlášť, search na jméno
  hostu tiše přestane fungovat. Stálo by za to přidat `names` do deps
  (`useListControls` by vzal `deps` navíc), nebo aspoň komentář přímo
  u `useListControls`.
- Selhání `api.hosts()` je zachycené a stránka se načte i tak.
- Search zahrnuje `detail` a jméno hostu a testy pokrývají oba směry.
