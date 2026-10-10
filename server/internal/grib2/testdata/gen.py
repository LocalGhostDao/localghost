#!/usr/bin/env python3
# Generates the GRIB2 fixtures with ecCodes (pip install eccodes), once, by hand: the files and
# expected.json.gz beside them. The Go tests read both; ecCodes is not needed to run them.
import bz2, gzip, json, math, random
import eccodes as ec

random.seed(7)
NI, NJ = 41, 33          # 0.25° from 50N 10W to 42N 0E
LA1, LO1, DI = 50.0, -10.0, 0.25

def field(kind):
    vals = []
    for j in range(NJ):
        lat = LA1 - j * DI
        for i in range(NI):
            lon = LO1 + i * DI
            if kind == "smooth":
                v = 283.15 + 6 * math.sin(math.radians(lat * 7)) * math.cos(math.radians(lon * 9)) + 0.3 * random.random()
            elif kind == "rough":
                v = 280 + 25 * random.random() - 12.5 + (3 if (i * j) % 7 == 0 else 0)
            elif kind == "precip":
                v = max(0.0, 2.5 * math.sin(math.radians(lat * 11)) + 1.5 * random.random() - 1.2)
            elif kind == "const":
                v = 273.15
            vals.append(v)
    return vals

def write(name, vals, packing, bits=16, decimal=0, extra=None, template=None, bitmap=None, multi=None, bz=False):
    out = []
    h = ec.codes_grib_new_from_samples("GRIB2")
    ec.codes_set(h, "discipline", 0)
    ec.codes_set(h, "centre", 98)
    ec.codes_set(h, "dataDate", 20261010)
    ec.codes_set(h, "dataTime", 600)
    ec.codes_set(h, "gridType", "regular_ll")
    ec.codes_set(h, "Ni", NI); ec.codes_set(h, "Nj", NJ)
    ec.codes_set(h, "latitudeOfFirstGridPointInDegrees", LA1)
    ec.codes_set(h, "longitudeOfFirstGridPointInDegrees", LO1)
    ec.codes_set(h, "latitudeOfLastGridPointInDegrees", LA1 - (NJ - 1) * DI)
    ec.codes_set(h, "longitudeOfLastGridPointInDegrees", LO1 + (NI - 1) * DI)
    ec.codes_set(h, "iDirectionIncrementInDegrees", DI)
    ec.codes_set(h, "jDirectionIncrementInDegrees", DI)
    ec.codes_set(h, "jScansPositively", 0)
    if template is not None:
        ec.codes_set(h, "productDefinitionTemplateNumber", template)
    ec.codes_set(h, "parameterCategory", extra.get("cat", 0) if extra else 0)
    ec.codes_set(h, "parameterNumber", extra.get("num", 0) if extra else 0)
    ec.codes_set(h, "typeOfFirstFixedSurface", extra.get("sfc", 103) if extra else 103)
    ec.codes_set(h, "scaledValueOfFirstFixedSurface", extra.get("lev", 2) if extra else 2)
    ec.codes_set(h, "scaleFactorOfFirstFixedSurface", 0)
    if template == 8:
        ec.codes_set(h, "stepType", "accum")
        ec.codes_set(h, "stepRange", extra["range"])
    else:
        ec.codes_set(h, "stepType", "instant")
        ec.codes_set(h, "step", extra.get("step", 6) if extra else 6)
    if bitmap is not None:
        ec.codes_set(h, "missingValue", 9999.0)
        ec.codes_set(h, "bitmapPresent", 1)
        vals = [9999.0 if k in bitmap else v for k, v in enumerate(vals)]
    ec.codes_set(h, "packingType", packing)
    ec.codes_set(h, "bitsPerValue", bits)
    ec.codes_set(h, "decimalScaleFactor", decimal)
    if packing == "grid_complex_spatial_differencing" and extra and "order" in extra:
        ec.codes_set(h, "orderOfSpatialDifferencing", extra["order"])
    ec.codes_set_values(h, vals)
    msg = ec.codes_get_message(h)
    got = list(ec.codes_get_values(h))
    if bitmap is not None:
        got = [None if k in bitmap else v for k, v in enumerate(got)]
    info = {"packingType": ec.codes_get(h, "packingType"), "bitsPerValue": ec.codes_get(h, "bitsPerValue"),
            "Ni": NI, "Nj": NJ, "la1": LA1, "lo1": LO1, "step": ec.codes_get(h, "step") if template != 8 else ec.codes_get(h, "stepRange"),
            "category": ec.codes_get(h, "parameterCategory"), "number": ec.codes_get(h, "parameterNumber"),
            "values": got}
    if packing == "grid_ccsds":
        info["ccsdsFlags"] = ec.codes_get(h, "ccsdsFlags"); info["ccsdsBlockSize"] = ec.codes_get(h, "ccsdsBlockSize"); info["ccsdsRsi"] = ec.codes_get(h, "ccsdsRsi")
    if packing.startswith("grid_complex"):
        info["dataRepresentationTemplateNumber"] = ec.codes_get(h, "dataRepresentationTemplateNumber")
    ec.codes_release(h)
    data = msg
    if multi:
        for (cat, num, step) in multi:
            h2 = ec.codes_new_from_message(msg)
            ec.codes_set(h2, "parameterCategory", cat); ec.codes_set(h2, "parameterNumber", num); ec.codes_set(h2, "step", step)
            data += ec.codes_get_message(h2)
            ec.codes_release(h2)
        info["messages"] = 1 + len(multi)
    if bz:
        data = bz2.compress(data)
    open(name, "wb").write(data)
    return info

exp = {}
smooth, rough, precip, const = field("smooth"), field("rough"), field("precip"), field("const")
exp["simple.grib2"] = write("simple.grib2", smooth, "grid_simple", bits=16, decimal=1)
exp["simple12.grib2"] = write("simple12.grib2", rough, "grid_simple", bits=12)
exp["const.grib2"] = write("const.grib2", const, "grid_simple", bits=16)
exp["bitmap.grib2"] = write("bitmap.grib2", smooth, "grid_simple", bits=16, decimal=2, bitmap=set(range(0, NI * NJ, 13)))
exp["complex.grib2"] = write("complex.grib2", rough, "grid_complex", bits=16, decimal=1)
exp["complex_sd1.grib2"] = write("complex_sd1.grib2", rough, "grid_complex_spatial_differencing", bits=16, decimal=1, extra={"order": 1})
exp["complex_sd2.grib2"] = write("complex_sd2.grib2", smooth, "grid_complex_spatial_differencing", bits=16, decimal=2, extra={"order": 2})
exp["complex_sd2_precip.grib2"] = write("complex_sd2_precip.grib2", precip, "grid_complex_spatial_differencing", bits=12, decimal=2, extra={"order": 2, "cat": 1, "num": 8, "sfc": 1, "lev": 0, "range": "3-6"}, template=8)
exp["ccsds.grib2"] = write("ccsds.grib2", smooth, "grid_ccsds", bits=16, decimal=2)
exp["ccsds_rough.grib2"] = write("ccsds_rough.grib2", rough, "grid_ccsds", bits=20, decimal=3)
exp["ccsds_precip.grib2"] = write("ccsds_precip.grib2", precip, "grid_ccsds", bits=8, decimal=1)
exp["ccsds_const.grib2"] = write("ccsds_const.grib2", const, "grid_ccsds", bits=16)
exp["ccsds_bitmap.grib2"] = write("ccsds_bitmap.grib2", smooth, "grid_ccsds", bits=16, decimal=2, bitmap=set(range(5, NI * NJ, 17)))
exp["multi.grib2"] = write("multi.grib2", smooth, "grid_simple", bits=16, decimal=1, multi=[(0, 6, 6), (2, 2, 9), (2, 3, 9)])
exp["icon.grib2.bz2"] = write("icon.grib2.bz2", smooth, "grid_simple", bits=16, decimal=1, bz=True)
with gzip.open("expected.json.gz", "wt") as f:
    json.dump(exp, f)
print("wrote", len(exp))
