# The weather, forecast by the box

A specification, written 10 October 2026, for the weather built from the forecast models' own
grids rather than taken from an API; the first layer (the pull, the decoder, the index) was
built the same day and is in wisp 0.0.7 (the section "Built" at the end says what and where).
The box pulls the grids itself,
straight from the weather centres, as it pulls the exchanges' tickers; the mirror
([MIRROR.md](MIRROR.md)) is not in the path. The idea is the crypto index's: the exchanges publish their
tickers, the box blends them into a price of its own and says how far they disagreed; the
weather centres publish their model grids, the box blends them into a forecast of its own and
says how far they disagreed.

## Why

Since 3 October 2026 the box pulls the forecast of some six thousand places from Open-Meteo,
the same list for everyone, so that no service learns where the person is. That solved the
privacy problem and left the dependency: a third party's API, its free tier for
non-commercial use, its limits, its shape, its uptime, and a forecast whose making the box
cannot explain. Open-Meteo is itself a reading of the public grids below. Reading them
ourselves gives a forecast with no key, no quota, no terms beyond attribution, every place at
once, and the models named on the card.

The box pulls them directly. The centres see the box's address and that it asked for the
same global surface fields every box asks for, which is what Open-Meteo sees today and what
the exchanges see of the tickers; nothing about the person is in the request, and nothing
about the person changes it. The mirror was considered as the place to decode and reduce the
grids once for every box, and is not needed: the work is a minute on a box, and a box that
pulls its own data trusts the centre's TLS and nothing in between, as it does the exchanges.
Should the fleet grow to where a thousand boxes pulling the same gigabyte from ECMWF is a
discourtesy, the same tool runs on the mirror and publishes the reduced cells as a set; the
format below is written so that move changes nothing on the box but the source.

## The sources, first party

Each is a numerical weather model run by a public weather centre and published as grids.
The terms are as read on 10 October 2026 and are to be read again before the pull is built;
each centre's attribution goes on the INTEGRATIONS card and in a `NOTICE-weather.txt` on the
volume, as the mirror's sets carry theirs.

| model | centre | grid | steps | runs | terms |
|---|---|---|---|---|---|
| IFS (and AIFS) | ECMWF | 0.25°, global | hourly to 3-hourly to 144 h (00/12 to 360 h) | 00, 06, 12, 18 | CC-BY-4.0, commercial use allowed with attribution; the last 12 runs kept online; GRIB2 with CCSDS packing |
| ICON-EU | DWD | about 0.0625°, Europe | hourly to 78 h, then 3-hourly to 120 h | 8 a day | DWD open data (GeoNutzV, attribution), bz2-compressed GRIB2, one file per parameter per step |
| ICON | DWD | 13 km, global | 3-hourly | 4 a day | as ICON-EU |
| GFS | NOAA | 0.25°, global | hourly to 120 h | 00, 06, 12, 18 | public domain; a filter service cuts by parameter and region |
| UKV, global | Met Office DataHub | 2 km UK (an equal-area projection, or a lat-lon cut), 10 km global | hourly to 54 h | hourly (UKV), 00/06/12/18 (global) | needs an account and a key; the free plan is 1 GB a month (enough to try, not to run), £15 a month buys 10 GB, enough for one region four runs a day; a box's own key, never shared |
| ICON-D2 | DWD | 2.2 km, Germany and its neighbours (43°N to 58°N, 4°W to 20°E) | hourly to 48 h | 8 a day | as ICON-EU; pulled only while the phone is in its domain |

The first cut takes IFS, ICON-EU and GFS: a global model with the best skill, the best
European model (where the first boxes are), and a second global model so the blend has a
third voice everywhere. ICON-D2 joins while the phone is in its domain, for the forecast where
the person is and the cells around them. The Met Office is next for a box whose owner has
put a key in SETTINGS, for the UK; AIFS and ICON global are later.

Getting a Met Office key: register at datahub.metoffice.gov.uk, choose the Atmospheric
Models data, take a plan (Free is 1 GB a month; a Monthly Subscription starts at £15 for
10 GB), make an order (the UK 2 km model on the latitude-longitude grid with a region around
where the box's person lives, the six fields above, hourly steps), and the API key is under My
Subscriptions. The box will take the key and the order's name in SETTINGS › SERVER once the
pull is written; the DataHub's documentation is behind its login, so that pull is written
against the API as the key holder sees it.

Sources: [ECMWF open data](https://www.ecmwf.int/en/forecasts/datasets/open-data), the
[DWD open-data tooling notes](https://www.ki-syndikat.de/tools/dwd-opendata/), the
[IEA Wind list of free NWP data](https://iea-wind.org/task51/task51-information-portal/free-nwp-data/).

## What is taken from each run

Five fields at the surface, the ones the card shows and the code needs:

- 2 m temperature
- total precipitation (accumulated from the run's start; the hourly amount is the difference
  between steps) and, where the model gives it, snowfall
- 10 m wind, u and v (speed and direction from them)
- total cloud cover
- the model's own orography, once per model (it changes only with the model), for the height
  correction below

For the steps the model has, out to 72 h (the three days the box keeps now) and no further at
first; the card has nothing to show past three days and the pull stays small.

## The pull, on the box

`ghost.tallyd`'s weather loop changes from "pull a hundred places from Open-Meteo every two
minutes" to "pull each model's new run and reduce it to the cells", through the box's
outbound client (`internal/egress`, the centres' hosts on its allowlist, TLS only, no
redirect off the list). Four times a day, as each run appears (a run is published some hours
after its time; the loop checks hourly and takes what is new):

1. **Fetch** the fields above for the run, by byte range where the centre offers an index
   (ECMWF's `.index` files; NOAA's filter), by file where it does not (DWD's one file per
   parameter per step). Taken 3-hourly from every model at first (the hour in between is
   interpolated), the run is some 150 MB from IFS, 200 MB from ICON-EU and 250 MB from GFS,
   about 600 MB a build and 2.5 GB a day; a setting takes the 00 and 12 runs only for a box
   on a metered line, half of that. A field is streamed, decoded, sampled and dropped, so the
   pull holds one field in memory at a time (4 MB for a global 0.25° field).
2. **Decode** GRIB2. The decoder is ours, standard library only, in `internal/grib2`: the
   sections, the grid definitions the three models use (regular latitude-longitude), and the
   data templates they use, simple packing (5.0), complex packing with spatial differencing
   (5.2, 5.3, GFS), and CCSDS (5.42, ECMWF since July 2023, the Rice coding of CCSDS 121.0-B).
   JPEG 2000 packing (5.40) is out of scope; a field packed that way is not taken. DWD's
   bz2 is `compress/bzip2`. One small file per model checked in as a fixture, decoded in the
   tests against values read by an independent tool once by hand.
3. **Reduce** to the cells. The list is the one the box has now (the half-degree cells of the
   world, each cell's largest town of fifteen thousand or more, the largest MaxPlaces of them,
   `internal/weather`); each model's grid is sampled at each cell's town, bilinear over the
   four grid points around it, for every step. A cell is keyed by its row and column in the
   half-degree grid, so a geo update that changes a cell's town keeps the cell.
4. **Keep** the reduced run on the volume, `<mount>/weather/<model>-<run>.wx`, a few
   megabytes: a header (format version, model, run time, the list's hash, the steps, the
   fields, the model's orography at each cell), then the values as int16 at a fixed scale per
   field (temperature in hundredths of a degree, precipitation in hundredths of a millimetre,
   wind in tenths of a metre a second, cloud in percent), row-major by cell then step,
   zstd-framed (`internal/zstd`). Six thousand cells, twenty-five steps, five fields, two
   bytes: 1.5 MB before compression. The last two runs of each model are kept; a restart
   recomputes from them without a fetch, and the same file is what a mirror set would carry,
   should that day come.

## The box's part, the index

With a model's new run reduced, tallyd computes, per cell, per hour of the next 72:

1. **Time.** A model with 3-hourly steps is interpolated to the hour linearly for
   temperature, wind and cloud; precipitation is spread evenly over the hours of its
   accumulation.
2. **Height.** The model's orography at the cell is rarely the town's height. The cell's town
   has a height from the box's own heights (the Copernicus packs, `internal/dem`); the
   temperature is moved by the standard lapse rate, 6.5 °C per kilometre of the difference,
   the same correction every forecast service applies and the reason a town in a valley is
   not given its ridge's temperature. Without the heights on the box, no correction, and the
   card says so.
3. **Blend.** For each field and hour, the models are combined with weights by region and
   lead time: ICON-EU carries the most weight inside its domain to 78 h, IFS the most
   elsewhere, GFS the least everywhere; past 78 h (not in the first cut) ICON-EU drops out.
   The blend is the weighted mean after dropping any model farther than a bound from the
   others' median (a model alone in saying it will be 8° colder is not averaged in but noted),
   the crypto index's rule. Precipitation blends the amounts and takes the probability as the
   share of models that give more than 0.1 mm in the hour, weighted; that share is the
   "chance of rain" the card draws, honest about what it is (the models' agreement, not a
   calibrated probability, until there are observations to calibrate against).
4. **Spread.** The disagreement is kept with every value (the range of the models'
   temperatures, say), as the crypto index keeps its spread, and the card can show it as a
   band on the line and a word ("the models agree", "the models differ by 4°").
5. **The code.** The weather word and the icon come from the blended fields by a fixed table
   (cloud cover for clear, partly cloudy, overcast; precipitation rate and temperature for
   drizzle, rain, heavy rain, sleet, snow; wind for the gale words); the same WMO-like code
   numbers the phone already maps, so `HomeText.weatherWord` and the icons stay.
6. **The days.** Max and min, the day's code and rain chance, from the hours, as the phone
   expects them.

The result is written into the same forecast rows the phone reads now (`Forecast.Hours`,
`Current`, the days, `fetchedAt`), plus a `source` line naming the models and runs and a
`spread` per hour, so nothing on the phone changes for the first release and the card gains
the band and the names when it is ready. "Pulled" becomes "the box's forecast from IFS 06Z,
ICON-EU 09Z and GFS 06Z, 2 h old".

The list is still the same for everyone and the box still computes every cell; nothing about
where the person is leaves the box, and nothing about it changes which cells are computed
first (all of them, in one pass, a few seconds).

## The way Google does it, and the open version

Google's weather page says what its forecast is made of: an internal forecasting system
over the weather models and the observations of the global agencies (DWD, ECMWF, NOAA, the
Met Office, Environment Canada, EUMETNET, Unidata); a nowcast of precipitation to twelve
hours from radar and the models; the person's own reports of the sky as feedback; climate
context from NOAA's records; and separate models for air quality and pollen over sensors,
dispersion models and satellites. The shape is the right one and the box can take it layer
by layer, with the open sources in place of the licensed ones and with everything that is
about the person staying on the box.

| Google's layer | what it is made of | the box's version | when |
|---|---|---|---|
| the forecast | models blended with observations | the index above: IFS, ICON-EU, GFS, the Met Office with a key, blended per cell with the spread kept | first |
| observations | the agencies' station networks | NOAA's METAR feed (public domain, worldwide airports, hourly), DWD's station data (open), the Met Office's with the key: the index verified and its weights set against them; the phone's own barometer as one more station that never leaves the box | second |
| the nowcast | radar composites, extrapolated, with the models | where radar is open: DWD's composite (Germany, every five minutes, a megabyte), NOAA's MRMS (the United States), the Met Office's with the key; the box pulls the whole national composite, as it pulls every cell, and moves the rain field forward an hour or two itself; EUMETNET's European composite is licensed, not open, so the nowcast is per country | third |
| the person's reports | "is it raining where you are" | the check-in's weather words, kept on the box, compared with the forecast for the day's cell, a running honesty score the DAY story can quote | third |
| climate context | NOAA's records | NOAA's GHCN-daily (open, the stations' daily history to the present): the record and the normal for the date at the nearest station, "unusually warm for October" | later |
| air quality | reference stations, sensor networks, dispersion models, satellites | Sensor.Community's open sensors (no key), the EEA's stations (open), Copernicus CAMS needs an account so it is a key a person may add, like the Met Office's | later |
| pollen | CAMS and regional models | CAMS, the same account; nothing without it | later, if at all |

What Google has that the open version does not: the licensed radar over all of Europe, the
density of national station networks beyond the airports, and a million people's reports.
What the open version has that Google's does not: the person's position never leaves the
house, every number has a named source and a spread, and the forecast can be read off the
box with `ghost-cli ghost.tallyd weather` down to which model said what.

## Verification, the second layer

Every run's step 0 is the model's analysis, the best estimate of what the weather was at the
run time. Keeping each model's 24 h forecast for a cell and comparing it with the next day's
analyses gives a running error per model, per region and per field, on the box, from data it
already holds; the weights above start as a fixed table and become that error's inverse once
there is a month of it. The observations in the table above make the
probability calibrated and the index verifiable against the ground, as the crypto index is
against the exchanges; the METAR feed is one small file an hour for the whole world, the
first observation source to take.

## The transition

1. wisp 0.0.8 carries `internal/grib2`, the pull, the reduced-run file and the index in
   tallyd, with Open-Meteo still the source until the first run has been read on the box;
   from then the index is the source and the API is not asked. `GHOST_WEATHER_API=1` keeps
   the API for a box that wants it for one release, for comparison.
2. One release later the API path is removed, with the Open-Meteo paragraphs in
   `internal/weather`, the INTEGRATIONS card, the explainer and the site's privacy page
   updated: the box talks to ECMWF, DWD and NOAA, for the same grids every box takes.

## Sizes and effort

The decoder is the real work: the GRIB2 sections and the regular grid are a day; simple and
complex packing with spatial differencing two more; CCSDS (Rice) decoding two more, with the
fixtures; the pull with the three centres' layouts a day; the reducer and the run file a day;
the index in tallyd two. Two to three weeks of evenings, most of it testable offline (the
fixtures, the pure blend, the run file's round-trip). The mirror's daemon in MIRROR.md is a
separate piece of work and neither waits for the other.

## Built

In wisp 0.0.7, the first layer:

- `internal/grib2`: the reader (sections, template 3.0, templates 4.0 and 4.8, packings 5.0,
  5.2, 5.3 and 5.42 with the CCSDS Rice decoder, bitmaps), checked against ecCodes over
  nineteen fixtures (`testdata/gen.py` made them; the expected values are ecCodes' own). A
  global 0.25° field decodes in 60 ms.
- `internal/nwp`: the catalogue (ICON-EU, IFS, GFS; their URL layouts, runs, delays, domain,
  weights), the pull (ECMWF's and NOAA's byte ranges from their indexes, DWD's bz2 files; the
  centre's hosts are fixed in `nwp.Bases`; a pause between requests), the reduction to the
  cells (bilinear), the run file (`<mount>/weather/runs/<model>-<YYYYMMDDHH>.wx`, gob of int16
  values, gzipped, the newest two of each model kept), the index (`Compute`: the 3-hourly
  steps interpolated, the precipitation from each model's cumulative curve with GFS's buckets
  summed, the lapse-rate correction, the blend with the outlier rule and the spread, the
  weather code table, the days, the sun). The steps are 3-hourly to 120 h, so three days of
  hours from today's midnight are filled whatever the run's age; the run before the newest
  serves the hours before the newest run's start.
- `ghost.tallyd`'s `nwp.go`: a look every half hour for a run newer than the one on the box,
  pulled when the centre has published its last step, the index computed after any new run
  and once an hour otherwise (for the hour "now"), written into `weather_places` through
  `weather.Save`, so the phone, the chat and INTEGRATIONS read it as they did the pull. A
  marker (`<mount>/weather/index.json`) stands the API pull down while the index is under
  36 h old; `GHOST_WEATHER_API=1` keeps it. `ghost-cli ghost.tallyd weather` shows the index
  beside the table; `weather nwp=1` pulls and computes now; health.sh has a line.
- The rows gained `source` (the runs) and `spreadC` per hour; HOME's card names the runs
  under the line; the chat's Describe says "the box's own forecast from …".

The second drop the same day: ICON-EU hourly to 48 h (3-hourly after, four runs a day);
ICON-D2 as a regional model, pulled while the phone's fix is in its domain; every run keeps
a window of the model's own grid around the fix (0.6° either side), and `ComputeAt` reads
the forecast at the exact point from those windows with the ground's height from the DEM
there, written as the HereID row (geonameid -1) named after the nearest listed town, so
`/v1/weather` at the phone's fix answers from it ("where you are, near Neu-Ulm · the box's
own forecast from ICON-D2 09Z, …"); `GHOST_WEATHER_RUNS=2` for a metered line.

The third drop: the Met Office pull (`internal/nwp/metoffice.go`) behind the key and order
the person puts in SETTINGS › YOUR BOX › MET OFFICE, the UK 2 km lat-lon model as the keyed,
regional "ukv"; the box names where home is (the trail's nights) so the order's region can be
drawn around it.

Not yet: the observations, the nowcast, the band drawn on the line, the API path's removal,
verification feeding the weights.

## Decided, open

Decided: the box pulls the grids and computes the forecast itself, no mirror in the path; no
key but the one a person chooses to add for the Met Office; the list and its order are the
same for everyone; the models are named on the card; spread is kept, never hidden behind a
single number.

Open: whether to carry 2 m dewpoint and gusts (the card does not show them; the day story
might); the exact weights table before there is verification; how long the reduced runs are
kept on the box (two days proposed); whether the Met Office's global model is worth the key,
or only the UK's 2 km.
