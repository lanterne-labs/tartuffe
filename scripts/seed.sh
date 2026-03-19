#!/bin/bash
# Seed French politicians for MVP deployment.
# Run via: ./scripts/seed.sh
# Or on the host: host run bash scripts/seed.sh
set -e

DM="${DM_BIN:-./dm}"

echo "=== Seeding persons ==="
$DM persons add "Marine Le Pen"
$DM persons add "Emmanuel Macron"
$DM persons add "Jean-Luc Mélenchon"
$DM persons add "Jordan Bardella"
$DM persons add "Gabriel Attal"
$DM persons add "François Bayrou"
$DM persons add "Éric Ciotti"
$DM persons add "Rassemblement National"  # party
$DM persons add "Renaissance"              # party
$DM persons add "La France Insoumise"      # party

echo ""
echo "=== Adding humans to search queue ==="
$DM queue add "Marine Le Pen"
$DM queue add "Emmanuel Macron"
$DM queue add "Jean-Luc Mélenchon"
$DM queue add "Jordan Bardella"
$DM queue add "Gabriel Attal"
$DM queue add "François Bayrou"
$DM queue add "Éric Ciotti"

echo ""
echo "=== Auto-affiliating persons with parties via Wikidata ==="
$DM persons affiliate

echo ""
echo "=== Fetching photos ==="
$DM plugin photos search --all

echo ""
echo "=== Linking Twitter handles ==="
$DM plugin twitter add --person "Marine Le Pen" --handle "MLP_officiel"
$DM plugin twitter add --person "Emmanuel Macron" --handle "EmmanuelMacron"
$DM plugin twitter add --person "Jean-Luc Mélenchon" --handle "JLMelenchon"
$DM plugin twitter add --person "Jordan Bardella" --handle "J_Bardella"
$DM plugin twitter add --person "Gabriel Attal" --handle "GabrielAttal"
$DM plugin twitter add --person "François Bayrou" --handle "bayabordeaux"
$DM plugin twitter add --person "Éric Ciotti" --handle "ECiotti"

echo ""
echo "=== Seed complete ==="
$DM queue list
