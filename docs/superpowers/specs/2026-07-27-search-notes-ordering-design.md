# Ordinamento e date in `search_notes`

**Data:** 27 luglio 2026
**Stato:** approvato per la pianificazione

## Obiettivo

Correggere il tool MCP `search_notes` affinché possa chiedere a Trilium ETAPI
di ordinare i risultati per data di modifica, conservare l'ordine ricevuto e
restituire le date esatte insieme ai dati sintetici delle note.

La correzione deve produrre un binario Linux AMD64 verificato da GitHub Actions
prima di qualsiasi sostituzione sul QNAP.

## Contesto e causa radice

Il binario distribuito deriva dal repository
`https://github.com/OVDEN13/trilium-mcp`. La provenienza più probabile è il tag
`v0.1.5`, commit `b4f2c53459825a997973af69085c2eab215c1058`, perché il repository
locale `gipasoft/trilium-mcp-inspector` lo costruisce con
`go install github.com/OVDEN13/trilium-mcp@latest` e quello era il tag più
recente al momento della build. L'hash del binario QNAP non è stato acquisito,
quindi il commit distribuito non è considerato verificato crittograficamente.

Lo schema live del proxy espone 16 tool. `search_notes` accetta attualmente:

- `query`, obbligatorio;
- `ancestor_note_id`;
- `fast_search`;
- `include_archived`;
- `limit`.

La risposta live contiene `note_id`, `title`, `type` e `attributes`, ma non
contiene date. Una query letterale
`@orderBy=-dateModified @limit=5` restituisce zero risultati perché viene
interpretata come espressione di ricerca Trilium.

Nel sorgente, il client ETAPI possiede già i campi `OrderBy`,
`OrderDirection` e `Limit` e li converte nei parametri HTTP `orderBy`,
`orderDirection` e `limit`. Anche il tipo `Note` deserializza già
`dateModified` e `utcDateModified`. Il difetto è nell'adattatore MCP:

1. lo schema non espone i due argomenti di ordinamento;
2. l'handler non li trasferisce a `SearchOpts`;
3. la proiezione sintetica dei risultati elimina entrambe le date.

## Approcci valutati

### Estendere soltanto l'adattatore MCP (scelto)

Si espongono argomenti espliciti e validati, si riusano i campi ETAPI già
presenti e si aggiungono le date alla risposta sintetica. È la modifica più
piccola che corregge la causa radice e conserva ETAPI come autorità
dell'ordinamento.

### Inoltrare liberamente `orderBy`

Permetterebbe tutti i campi e le label supportati da ETAPI, ma amplierebbe il
contratto MCP senza necessità e accetterebbe valori arbitrari. Questo approccio
non viene adottato.

### Ordinare localmente dopo la risposta ETAPI

È scorretto quando ETAPI applica `limit` prima dell'ordinamento locale: il tool
potrebbe ordinare soltanto un sottoinsieme che non contiene le note realmente
più recenti. Questo approccio non viene adottato.

## Contratto MCP

`search_notes` conserva gli argomenti esistenti e aggiunge:

- `order_by`, enum opzionale:
  - `dateModified`;
  - `utcDateModified`;
- `order_direction`, enum opzionale:
  - `asc`;
  - `desc`.

`limit` rimane opzionale, usa il default esistente `50` ed è validato come
intero compreso tra `1` e `200`.

`query` rimane obbligatorio e non vuoto per compatibilità con il contratto
attuale e con ETAPI. La descrizione del tool documenta
`note.noteId != ""` come espressione per selezionare tutte le note, necessaria
per richieste generiche come "le cinque note modificate più di recente".

Una richiesta tipica diventa:

```json
{
  "query": "note.noteId != \"\"",
  "order_by": "dateModified",
  "order_direction": "desc",
  "limit": 5
}
```

I nomi MCP usano lo stile `snake_case` già adottato dal server; l'handler li
converte nei nomi ETAPI `camelCase`.

## Flusso dei dati

1. Il client MCP valida gli argomenti rispetto allo schema.
2. L'handler applica una seconda validazione deterministica, così chiamate che
   aggirano la validazione dello schema ricevono comunque un errore chiaro.
3. L'handler costruisce `SearchOpts` senza modificare la query.
4. `Client.SearchNotes` genera una richiesta di sola lettura:
   `GET /etapi/notes`.
5. ETAPI filtra, ordina e limita i risultati.
6. L'handler conserva esattamente l'ordine della slice ricevuta.
7. La risposta sintetica include, per ogni nota:
   - `note_id`;
   - `title`;
   - `type`;
   - `attributes`, quando presenti;
   - `date_modified`, quando ETAPI fornisce `dateModified`;
   - `utc_date_modified`, quando ETAPI fornisce `utcDateModified`.

Le date assenti restano assenti. Il server non le calcola, converte o inventa.

## Validazione ed errori

- `order_by` diverso dai due valori consentiti produce un tool error chiaro e
  non effettua richieste ETAPI.
- `order_direction` diverso da `asc` o `desc` produce un tool error chiaro e
  non effettua richieste ETAPI.
- `limit` non intero o fuori dall'intervallo `1..200` produce un tool error
  chiaro e non effettua richieste ETAPI.
- Una chiamata con i soli argomenti già esistenti mantiene il comportamento
  precedente, incluso il default `limit=50`.
- Nessun errore o log deve includere token, header Authorization o URL privati.
- `search_notes` non invoca endpoint mutativi.

## Strategia di test

I test vengono scritti prima dell'implementazione e usano esclusivamente server
HTTP simulati.

Copertura minima:

1. lo schema dichiara i due enum e i vincoli di `limit`;
2. `order_by=dateModified`, `order_direction=desc` e `limit=5` diventano
   `orderBy=dateModified`, `orderDirection=desc` e `limit=5`;
3. l'ordine restituito da ETAPI resta invariato;
4. entrambe le date vengono incluse nella risposta MCP;
5. date assenti non vengono aggiunte;
6. valori non validi di `order_by`, `order_direction` e `limit` vengono
   rifiutati prima della rete;
7. una chiamata con il solo `query` resta compatibile;
8. viene usato soltanto `GET /etapi/notes`;
9. token e URL privati non compaiono in log o messaggi di errore.

La regressione completa esegue:

```text
go vet ./...
go test -race ./...
go build -ldflags="-s -w" -o trilium-mcp .
smoke test MCP initialize
```

## Versione e GitHub Actions

Il server passa alla versione `0.1.6`.

Il fork `gipasoft/trilium-mcp` conserva i workflow upstream e aggiunge alla CI
un artefatto scaricabile per Linux AMD64:

- `trilium-mcp-linux-amd64`;
- `trilium-mcp-linux-amd64.sha256`.

La build usa `CGO_ENABLED=0`, `GOOS=linux` e `GOARCH=amd64`. L'artefatto viene
caricato soltanto dopo `go vet`, `go test -race`, build e smoke test riusciti.
Il nome o i metadati dell'artefatto devono permettere di risalire al commit
GitHub Actions che lo ha prodotto.

## Confini di distribuzione

Questa fase non modifica il QNAP.

Prima della distribuzione devono essere disponibili:

1. commit del fork;
2. esecuzione GitHub Actions completamente verde;
3. binario Linux AMD64 e relativo SHA-256;
4. evidenza dei test automatici;
5. procedura di backup e rollback del solo `bin/trilium-mcp`.

La successiva distribuzione potrà sostituire esclusivamente il binario Trilium,
ricreare soltanto il servizio proxy interessato e verificare Plex, Paperless e
Trilium. Token, URL, porte, rete e autenticazione restano fuori ambito.
