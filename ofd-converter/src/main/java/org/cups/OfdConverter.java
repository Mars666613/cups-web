package org.cups;

import org.ofdrw.converter.ConvertHelper;
import java.nio.file.Paths;

/**
 * OFD to PDF command-line converter using ofdrw-converter.
 *
 * Usage: java -jar ofd-converter.jar <input.ofd> <output.pdf>
 */
public class OfdConverter {
    public static void main(String[] args) {
        if (args.length < 2) {
            System.err.println("Usage: java -jar ofd-converter.jar <input.ofd> <output.pdf>");
            System.exit(1);
        }

        String inputPath = args[0];
        String outputPath = args[1];

        try {
            ConvertHelper.toPdf(Paths.get(inputPath), Paths.get(outputPath));
        } catch (Exception e) {
            System.err.println("OFD to PDF conversion failed: " + e.getMessage());
            e.printStackTrace();
            System.exit(2);
        }
    }
}
