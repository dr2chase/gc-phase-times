# gc-phase-times

The `phase-times` command reads the output of the `cmpcl-phase.sh` script in
the [`bent` tool](https://github.com/dr2chase/bent), supplied either as a command argument
or on standard input, and generates a pair of CSV formatted files containing data that can
be used to check whether
[particular phases of gc's ssa back-end are unusually slow](https://docs.google.com/spreadsheets/d/1f1rTX73ett6iKMb5LuNpnG78T7CLucQAHRKBZuI23Q4/edit?usp=sharing).

It's relatively sensitive to the input format, but the parsing part is also not too exotic.
The names of the CSV output files are derived from the configuration used for bent, for `cmpcl-phase.sh`
the files will be `Base.csv` and `Test.csv`.
